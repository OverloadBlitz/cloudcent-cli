package pulumi

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	internalmodel "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	awsdecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	pulumirpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// ResourceCollector is a gRPC ResourceMonitor server that intercepts all resources
// that's why not cloud creds are needed
type ResourceCollector struct {
	pulumirpc.UnimplementedResourceMonitorServer

	mu              sync.Mutex
	Resources       []internalmodel.ResourceRecord
	StackOutputs    map[string]string
	amiOSMap        map[string]string
	providerRegions map[string]string
	stackConfig     map[string]string
	stackURN        string
	resourceState   map[string]*structpb.Struct

	done chan struct{}
}

func NewResourceCollector() *ResourceCollector {
	return &ResourceCollector{
		amiOSMap:        make(map[string]string),
		providerRegions: make(map[string]string),
		StackOutputs:    make(map[string]string),
		resourceState:   make(map[string]*structpb.Struct),
		done:            make(chan struct{}),
	}
}

// SetStackConfig provides the resolved Pulumi stack config so the collector
// can fall back to config values (e.g. "aws:region") when provider inputs
// don't carry an explicit region.
func (c *ResourceCollector) SetStackConfig(cfg map[string]string) {
	c.mu.Lock()
	c.stackConfig = cfg
	c.mu.Unlock()
}

// InjectECSCrossResourceAttrs performs a post-collection pass that propagates
// TaskDefinition attributes (cpu, memory, runtimePlatform) into the
// MockedProperties of every ECS Service that references the definition.
//
// This mirrors the DynamoDB GSI pattern: the "child" resource (Service) reads
// attributes from its "parent" (TaskDefinition) via MockedProperties so that
// the decoder does not need to re-traverse the full resource graph.
//
// Call this once after the Pulumi program has finished running (i.e. after
// Wait() returns) and before passing records to DecodeAllResources.
func (c *ResourceCollector) InjectECSCrossResourceAttrs() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Build a name → TaskDefinition record index.
	// TaskDefinition resources are identified by their Pulumi resource name.
	taskDefs := make(map[string]*internalmodel.ResourceRecord)
	for i := range c.Resources {
		if c.Resources[i].Type == "aws:ecs/taskDefinition:TaskDefinition" {
			taskDefs[c.Resources[i].Name] = &c.Resources[i]
		}
	}

	if len(taskDefs) == 0 {
		return
	}

	for i := range c.Resources {
		rec := &c.Resources[i]
		if rec.Type != "aws:ecs/service:Service" {
			continue
		}

		// The "taskDefinition" input is typically an ARN or a resource name.
		// We try to match it against known TaskDefinition names.
		tdRef := awsdecode.ExtractInput(rec.Inputs, "taskDefinition")
		if tdRef == "" {
			continue
		}

		td := resolveTaskDefinition(tdRef, taskDefs)
		if td == nil {
			continue
		}

		if rec.MockedProperties == nil {
			rec.MockedProperties = make(map[string]string)
		}

		// cpu and memory are top-level inputs on TaskDefinition (e.g. "256", "512").
		if v := awsdecode.ExtractInput(td.Inputs, "cpu"); v != "" {
			rec.MockedProperties["taskCpu"] = v
		}
		if v := awsdecode.ExtractInput(td.Inputs, "memory"); v != "" {
			rec.MockedProperties["taskMemory"] = v
		}

		// runtimePlatform holds cpuArchitecture and operatingSystemFamily.
		if v := awsdecode.ExtractInput(td.Inputs, "runtimePlatform.cpuArchitecture"); v != "" {
			rec.MockedProperties["cpuArchitecture"] = v
		}
		if v := awsdecode.ExtractInput(td.Inputs, "runtimePlatform.operatingSystemFamily"); v != "" {
			rec.MockedProperties["osFamily"] = v
		}
	}
}

// resolveTaskDefinition attempts to find a TaskDefinition record that matches
// the given reference string. The reference may be:
//   - an exact resource name ("my-task-def")
//   - an ARN containing the name as a suffix ("arn:aws:ecs:...:task-definition/my-task-def:1")
//
// Returns nil when no match is found.
func resolveTaskDefinition(ref string, index map[string]*internalmodel.ResourceRecord) *internalmodel.ResourceRecord {
	// Exact name match.
	if td, ok := index[ref]; ok {
		return td
	}
	// ARN suffix match: "arn:aws:ecs:...:task-definition/<name>:<revision>".
	for name, td := range index {
		if strings.HasSuffix(ref, "/"+name) || strings.Contains(ref, "/"+name+":") {
			return td
		}
	}
	return nil
}

// Reset clears collected resources and stack outputs so the collector can be
// reused for a retry run (e.g. after auto-filling missing config keys).
func (c *ResourceCollector) Reset() {
	c.mu.Lock()
	c.Resources = nil
	c.StackOutputs = make(map[string]string)
	c.amiOSMap = make(map[string]string)
	c.providerRegions = make(map[string]string)
	c.resourceState = make(map[string]*structpb.Struct)
	c.stackURN = ""
	c.done = make(chan struct{})
	c.mu.Unlock()
}

// SupportsFeature handles the SDK handshake — just say yes to everything.
func (c *ResourceCollector) SupportsFeature(_ context.Context, req *pulumirpc.SupportsFeatureRequest) (*pulumirpc.SupportsFeatureResponse, error) {
	return &pulumirpc.SupportsFeatureResponse{HasSupport: true}, nil
}

// RegisterResource is the core method — called for every resource the user program creates.
func (c *ResourceCollector) RegisterResource(_ context.Context, req *pulumirpc.RegisterResourceRequest) (*pulumirpc.RegisterResourceResponse, error) {
	inputs := protoToPropertyMap(req.GetObject())
	urn := fmt.Sprintf("urn:pulumi:stack::project::%s::%s", req.GetType(), req.GetName())
	id := req.GetName() + "_id"

	// Track the stack URN so we can capture its outputs later.
	if req.GetType() == "pulumi:pulumi:Stack" {
		c.mu.Lock()
		c.stackURN = urn
		c.mu.Unlock()
	}

	if req.GetType() == "pulumi:providers:aws" {
		region := propertyString(inputs["region"])
		if region == "" {
			// Fall back to stack config: "aws:region" is the canonical key.
			c.mu.Lock()
			for _, key := range []string{"aws:region", "aws:config:region"} {
				if v := c.stackConfig[key]; v != "" {
					region = v
					break
				}
			}
			c.mu.Unlock()
		}
		if region != "" {
			c.mu.Lock()
			c.providerRegions[providerReference(urn, id)] = region
			c.mu.Unlock()
		}
	}

	// Track regions for Azure, GCP, and OCI providers.
	if req.GetType() == "pulumi:providers:azure-native" || req.GetType() == "pulumi:providers:azure" {
		region := propertyString(inputs["location"])
		if region == "" {
			c.mu.Lock()
			for _, key := range []string{"azure-native:location", "azure:location"} {
				if v := c.stackConfig[key]; v != "" {
					region = v
					break
				}
			}
			c.mu.Unlock()
		}
		if region != "" {
			c.mu.Lock()
			c.providerRegions[providerReference(urn, id)] = region
			c.mu.Unlock()
		}
	}

	if req.GetType() == "pulumi:providers:gcp" {
		region := propertyString(inputs["region"])
		if region == "" {
			region = propertyString(inputs["zone"])
		}
		if region == "" {
			c.mu.Lock()
			for _, key := range []string{"gcp:region", "gcp:zone"} {
				if v := c.stackConfig[key]; v != "" {
					region = v
					break
				}
			}
			c.mu.Unlock()
		}
		if region != "" {
			c.mu.Lock()
			c.providerRegions[providerReference(urn, id)] = region
			c.mu.Unlock()
		}
	}

	if req.GetType() == "pulumi:providers:oci" {
		region := propertyString(inputs["region"])
		if region == "" {
			c.mu.Lock()
			if v := c.stackConfig["oci:region"]; v != "" {
				region = v
			}
			c.mu.Unlock()
		}
		if region != "" {
			c.mu.Lock()
			c.providerRegions[providerReference(urn, id)] = region
			c.mu.Unlock()
		}
	}

	// Resolve OS for EC2 instances
	os := ""
	region := ""
	if req.GetType() == "aws:ec2/instance:Instance" {
		amiID := ""
		if v, ok := inputs["ami"]; ok && v.IsString() {
			amiID = v.StringValue()
		}
		c.mu.Lock()
		region = c.lookupProviderRegionLocked(req.GetProvider(), req.GetProviders())
		if amiID != "" {
			if inferredOS, found := c.amiOSMap[amiID]; found {
				os = inferredOS
			}
		}
		c.mu.Unlock()
	} else {
		// Resolve region for all other resource types.
		c.mu.Lock()
		region = c.lookupProviderRegionLocked(req.GetProvider(), req.GetProviders())
		c.mu.Unlock()

		// For Azure/GCP/OCI resources, also check the resource's own location input.
		if region == "" {
			for _, locKey := range []string{"location", "region", "zone", "availabilityDomain"} {
				if v, ok := inputs[resource.PropertyKey(locKey)]; ok && v.IsString() {
					region = v.StringValue()
					break
				}
			}
		}
	}

	c.mu.Lock()
	c.Resources = append(c.Resources, internalmodel.ResourceRecord{
		Type:   req.GetType(),
		Name:   req.GetName(),
		ID:     req.GetName() + "_id",
		Inputs: inputs,
		MockedProperties: map[string]string{
			"os":     os,
			"region": region,
		},
	})
	c.mu.Unlock()

	// Build enriched outputs: start from inputs, inject synthetic computed
	// fields (arn, invokeArn, endpoint, etc.) so downstream Output chains
	// can resolve instead of hanging forever.
	enrichedOutputs := enrichOutputs(req.GetObject(), req.GetType(), req.GetName(), urn, id)

	// Store state so pulumi:pulumi:getResource can return it later.
	c.mu.Lock()
	c.resourceState[urn] = enrichedOutputs
	c.mu.Unlock()

	return &pulumirpc.RegisterResourceResponse{
		Urn:    urn,
		Id:     id,
		Object: enrichedOutputs,
	}, nil
}

// RegisterResourceOutputs captures stack outputs (from pulumi.export(...) calls).
func (c *ResourceCollector) RegisterResourceOutputs(_ context.Context, req *pulumirpc.RegisterResourceOutputsRequest) (*emptypb.Empty, error) {
	c.mu.Lock()
	isStack := req.GetUrn() == c.stackURN
	c.mu.Unlock()

	if isStack && req.GetOutputs() != nil {
		outputs := protoToPropertyMap(req.GetOutputs())
		c.mu.Lock()
		for k, v := range outputs {
			key := string(k)
			if v.IsString() {
				c.StackOutputs[key] = v.StringValue()
			} else if v.IsNull() || v.IsComputed() || v.IsOutput() {
				// Computed/unknown at dry-run time — show placeholder.
				c.StackOutputs[key] = "(known after apply)"
			} else {
				c.StackOutputs[key] = fmt.Sprintf("%v", v)
			}
		}
		c.mu.Unlock()
	}
	return &emptypb.Empty{}, nil
}

// Invoke handles data lookups like ec2.LookupAmi — return mock outputs so the
// program can continue without hitting any cloud provider.
func (c *ResourceCollector) Invoke(_ context.Context, req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	tok := req.GetTok()
	fmt.Fprintf(os.Stderr, "[mock] Invoke token=%s\n", tok)
	switch tok {
	case "pulumi:pulumi:getResource":
		return c.invokeGetResource(req)
	case "aws:ec2/getAmi:getAmi":
		return c.invokeGetAmi(req)
	case "aws:ssm/getParameter:getParameter":
		return c.invokeGetSSMParameter(req)
	case "aws:ec2/getVpc:getVpc":
		return c.invokeGetVpc(req)
	case "aws:ec2/getSubnets:getSubnets":
		return c.invokeGetSubnets(req)
	case "aws:ec2/getSubnet:getSubnet":
		return c.invokeGetSubnet(req)
	case "aws:ecr/getAuthorizationToken:getAuthorizationToken":
		return c.invokeGetEcrAuthToken(req)
	case "aws:ec2/getSecurityGroup:getSecurityGroup":
		return c.invokeGetSecurityGroup(req)
	case "aws:iam/getPolicy:getPolicy",
		"aws:iam/getPolicyDocument:getPolicyDocument":
		return c.invokeGetIamPolicy(req)
	case "aws:s3/getBucketObject:getBucketObject",
		"aws:s3/getObject:getObject":
		return c.invokeGetS3Object(req)
	case "aws:region/getRegion:getRegion",
		"aws:getRegion:getRegion":
		return c.invokeGetRegion(req)
	case "aws:getCallerIdentity:getCallerIdentity",
		"aws:iam/getCallerIdentity:getCallerIdentity":
		return c.invokeGetCallerIdentity(req)
	case "aws:ec2/getAvailabilityZones:getAvailabilityZones",
		"aws:index/getAvailabilityZones:getAvailabilityZones":
		return c.invokeGetAvailabilityZones(req)
	case "aws:getPartition:getPartition":
		return c.invokeGetPartition(req)
	}

	// For any unhandled invoke, echo the request args back. This works for
	// many simple lookups but may lack required fields for complex ones.
	// If a new invoke causes hangs, add a dedicated handler above.
	return &pulumirpc.InvokeResponse{Return: req.GetArgs()}, nil
}

// invokeGetAmi infers OS from LookupAmi filters and returns a mock AMI ID.
// It scans all filter name/values pairs (not just name=="name") as well as
// the top-level nameRegex field, so patterns like "platform" filters and
// name-prefix patterns are all considered.
func (c *ResourceCollector) invokeGetAmi(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	os := ""
	args := req.GetArgs().GetFields()

	// Helper: try to infer OS from a string, keeping the first match found.
	tryInfer := func(s string) {
		if os == "" {
			if inferredOS, ok := awsdecode.InferOSFromPattern(s); ok {
				os = inferredOS
			}
		}
	}

	// 1. Top-level nameRegex field.
	if v, ok := args["nameRegex"]; ok {
		tryInfer(v.GetStringValue())
	}

	// 2. filters[].values[] — scan every filter's values, not just name=="name".
	if filtersVal, ok := args["filters"]; ok {
		for _, filterItem := range filtersVal.GetListValue().GetValues() {
			obj := filterItem.GetStructValue().GetFields()
			if valuesVal, ok := obj["values"]; ok {
				for _, v := range valuesVal.GetListValue().GetValues() {
					tryInfer(v.GetStringValue())
				}
			}
		}
	}

	mockAMIID := "ami-mock"
	mockAMIName := "mock-ami"
	if os != "" {
		mockAMIID = "ami-mock-" + strings.ToLower(os)
		mockAMIName = "mock-ami-" + strings.ToLower(os)
		c.mu.Lock()
		c.amiOSMap[mockAMIID] = os
		c.mu.Unlock()
	}

	ret := mustNewStruct(map[string]any{
		"id":   mockAMIID,
		"name": mockAMIName,
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// invokeGetSSMParameter infers OS from SSM parameter path (e.g. /aws/service/ami-amazon-linux-latest/...).
func (c *ResourceCollector) invokeGetSSMParameter(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	os := ""
	args := req.GetArgs().GetFields()
	if nameVal, ok := args["name"]; ok {
		if inferredOS, ok := awsdecode.InferOSFromPattern(nameVal.GetStringValue()); ok {
			os = inferredOS
		}
	}

	mockAMIID := "ami-mock"
	mockName := "mock-ssm"
	if os != "" {
		mockAMIID = "ami-mock-" + os
		mockName = "mock-ssm-" + os
		c.mu.Lock()
		c.amiOSMap[mockAMIID] = os
		c.mu.Unlock()
	}

	ret := mustNewStruct(map[string]any{
		"value": mockAMIID,
		"name":  mockName,
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// ---------------------------------------------------------------------------
// AWS VPC / Networking invoke mocks
// ---------------------------------------------------------------------------

// invokeGetVpc returns a mock VPC with the required id, cidrBlock, etc.
func (c *ResourceCollector) invokeGetVpc(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	args := req.GetArgs().GetFields()
	vpcID := "vpc-mock-00000001"
	if v, ok := args["id"]; ok && v.GetStringValue() != "" {
		vpcID = v.GetStringValue()
	}
	ret := mustNewStruct(map[string]any{
		"id":                 vpcID,
		"arn":                "arn:aws:ec2:us-east-1:123456789012:vpc/" + vpcID,
		"cidrBlock":          "172.31.0.0/16",
		"default":            true,
		"dhcpOptionsId":      "dopt-mock-00000001",
		"enableDnsHostnames": true,
		"enableDnsSupport":   true,
		"instanceTenancy":    "default",
		"mainRouteTableId":   "rtb-mock-00000001",
		"ownerId":            "123456789012",
		"state":              "available",
		"tags":               map[string]any{},
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// invokeGetSubnets returns a mock list of subnet IDs.
func (c *ResourceCollector) invokeGetSubnets(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	_ = req
	ret := mustNewStruct(map[string]any{
		"id":   "mock-subnets-query",
		"ids":  []any{"subnet-mock-a", "subnet-mock-b", "subnet-mock-c"},
		"tags": map[string]any{},
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// invokeGetSubnet returns a single mock subnet.
func (c *ResourceCollector) invokeGetSubnet(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	args := req.GetArgs().GetFields()
	subnetID := "subnet-mock-a"
	if v, ok := args["id"]; ok && v.GetStringValue() != "" {
		subnetID = v.GetStringValue()
	}
	ret := mustNewStruct(map[string]any{
		"id":                  subnetID,
		"arn":                 "arn:aws:ec2:us-east-1:123456789012:subnet/" + subnetID,
		"availabilityZone":    "us-east-1a",
		"availabilityZoneId":  "use1-az1",
		"cidrBlock":           "172.31.0.0/20",
		"vpcId":               "vpc-mock-00000001",
		"mapPublicIpOnLaunch": true,
		"state":               "available",
		"ownerId":             "123456789012",
		"tags":                map[string]any{},
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// ---------------------------------------------------------------------------
// AWS ECR invoke mocks
// ---------------------------------------------------------------------------

// invokeGetEcrAuthToken returns a mock ECR authorization token so that
// docker.Image and similar resources can resolve their Output chains
// without actually contacting ECR.
func (c *ResourceCollector) invokeGetEcrAuthToken(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	_ = req
	// The token is base64("mock-user:mock-password")
	mockToken := "bW9jay11c2VyOm1vY2stcGFzc3dvcmQ="
	ret := mustNewStruct(map[string]any{
		"authorizationToken": mockToken,
		"expiresAt":          "2099-12-31T23:59:59Z",
		"password":           "mock-password",
		"proxyEndpoint":      "https://123456789012.dkr.ecr.us-east-1.amazonaws.com",
		"userName":           "mock-user",
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// ---------------------------------------------------------------------------
// AWS Security Group invoke mock
// ---------------------------------------------------------------------------

func (c *ResourceCollector) invokeGetSecurityGroup(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	args := req.GetArgs().GetFields()
	sgID := "sg-mock-00000001"
	if v, ok := args["id"]; ok && v.GetStringValue() != "" {
		sgID = v.GetStringValue()
	}
	ret := mustNewStruct(map[string]any{
		"id":          sgID,
		"arn":         "arn:aws:ec2:us-east-1:123456789012:security-group/" + sgID,
		"name":        "mock-sg",
		"description": "mock security group",
		"vpcId":       "vpc-mock-00000001",
		"tags":        map[string]any{},
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// ---------------------------------------------------------------------------
// AWS IAM invoke mocks
// ---------------------------------------------------------------------------

func (c *ResourceCollector) invokeGetIamPolicy(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	args := req.GetArgs().GetFields()
	policyArn := "arn:aws:iam::aws:policy/MockPolicy"
	if v, ok := args["arn"]; ok && v.GetStringValue() != "" {
		policyArn = v.GetStringValue()
	}
	ret := mustNewStruct(map[string]any{
		"id":     policyArn,
		"arn":    policyArn,
		"name":   "MockPolicy",
		"path":   "/",
		"policy": `{"Version":"2012-10-17","Statement":[]}`,
		"json":   `{"Version":"2012-10-17","Statement":[]}`,
		"tags":   map[string]any{},
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// ---------------------------------------------------------------------------
// AWS S3 invoke mocks
// ---------------------------------------------------------------------------

func (c *ResourceCollector) invokeGetS3Object(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	args := req.GetArgs().GetFields()
	bucket := "mock-bucket"
	key := "mock-key"
	if v, ok := args["bucket"]; ok && v.GetStringValue() != "" {
		bucket = v.GetStringValue()
	}
	if v, ok := args["key"]; ok && v.GetStringValue() != "" {
		key = v.GetStringValue()
	}
	ret := mustNewStruct(map[string]any{
		"id":          bucket + "/" + key,
		"body":        "",
		"contentType": "application/octet-stream",
		"etag":        "mock-etag",
		"tags":        map[string]any{},
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// ---------------------------------------------------------------------------
// AWS account / region invoke mocks
// ---------------------------------------------------------------------------

func (c *ResourceCollector) invokeGetRegion(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	region := "us-east-1"
	c.mu.Lock()
	for _, key := range []string{"aws:region", "aws:config:region"} {
		if v := c.stackConfig[key]; v != "" {
			region = v
			break
		}
	}
	c.mu.Unlock()
	ret := mustNewStruct(map[string]any{
		"id":          region,
		"name":        region,
		"description": "Mock " + region,
		"endpoint":    "ec2." + region + ".amazonaws.com",
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

func (c *ResourceCollector) invokeGetCallerIdentity(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	_ = req
	ret := mustNewStruct(map[string]any{
		"id":        "123456789012",
		"accountId": "123456789012",
		"arn":       "arn:aws:iam::123456789012:root",
		"userId":    "AIDAMOCKUSERID",
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

func (c *ResourceCollector) invokeGetAvailabilityZones(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	region := "us-east-1"
	c.mu.Lock()
	for _, key := range []string{"aws:region", "aws:config:region"} {
		if v := c.stackConfig[key]; v != "" {
			region = v
			break
		}
	}
	c.mu.Unlock()
	ret := mustNewStruct(map[string]any{
		"id":    region,
		"names": []any{region + "a", region + "b", region + "c"},
		"zoneIds": []any{
			strings.ReplaceAll(region, "-", "") + "-az1",
			strings.ReplaceAll(region, "-", "") + "-az2",
			strings.ReplaceAll(region, "-", "") + "-az3",
		},
		"state": "available",
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

func (c *ResourceCollector) invokeGetPartition(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	_ = req
	ret := mustNewStruct(map[string]any{
		"id":               "aws",
		"partition":        "aws",
		"dnsSuffix":        "amazonaws.com",
		"reverseDnsSuffix": "com.amazonaws",
	})
	return &pulumirpc.InvokeResponse{Return: ret}, nil
}

// ---------------------------------------------------------------------------
// pulumi:pulumi:getResource handler — resolves cross-resource Output refs
// ---------------------------------------------------------------------------

func (c *ResourceCollector) invokeGetResource(req *pulumirpc.ResourceInvokeRequest) (*pulumirpc.InvokeResponse, error) {
	args := req.GetArgs().GetFields()
	urn := ""
	if v, ok := args["urn"]; ok {
		urn = v.GetStringValue()
	}
	if urn == "" {
		return &pulumirpc.InvokeResponse{Return: req.GetArgs()}, nil
	}

	c.mu.Lock()
	state, ok := c.resourceState[urn]
	c.mu.Unlock()

	if ok && state != nil {
		// Wrap in the expected response shape: { "urn": ..., "id": ..., "state": {...} }
		ret := mustNewStruct(map[string]any{
			"urn":   urn,
			"id":    urn + "_id",
			"state": state.AsMap(),
		})
		return &pulumirpc.InvokeResponse{Return: ret}, nil
	}
	return &pulumirpc.InvokeResponse{Return: req.GetArgs()}, nil
}

// ---------------------------------------------------------------------------
// enrichOutputs injects synthetic computed fields into resource outputs
// ---------------------------------------------------------------------------

// enrichOutputs copies the original inputs and injects mock computed fields
// (arn, invokeArn, endpoint, etc.) that downstream resources may depend on.
// Without these, Output<T> chains hang forever waiting for values that only
// exist after a real provider Create/Read.
func enrichOutputs(original *structpb.Struct, resourceType, name, urn, id string) *structpb.Struct {
	fields := make(map[string]*structpb.Value)

	// Copy all original input fields.
	if original != nil {
		for k, v := range original.GetFields() {
			fields[k] = v
		}
	}

	// Inject id if not already present.
	if _, ok := fields["id"]; !ok {
		fields["id"] = structpb.NewStringValue(id)
	}

	// Inject a synthetic ARN for any AWS resource.
	mockARN := fmt.Sprintf("arn:aws:mock:us-east-1:123456789012:%s/%s", resourceType, name)
	if _, ok := fields["arn"]; !ok {
		fields["arn"] = structpb.NewStringValue(mockARN)
	}

	// Resource-type-specific computed outputs.
	switch {
	case strings.Contains(resourceType, "lambda") && strings.Contains(resourceType, "Function"):
		if _, ok := fields["invokeArn"]; !ok {
			fields["invokeArn"] = structpb.NewStringValue(
				fmt.Sprintf("arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/%s/invocations", mockARN))
		}
		if _, ok := fields["qualifiedArn"]; !ok {
			fields["qualifiedArn"] = structpb.NewStringValue(mockARN + ":$LATEST")
		}
		if _, ok := fields["version"]; !ok {
			fields["version"] = structpb.NewStringValue("$LATEST")
		}

	case strings.Contains(resourceType, "apigatewayv2") && strings.Contains(resourceType, "Api"):
		if _, ok := fields["apiEndpoint"]; !ok {
			fields["apiEndpoint"] = structpb.NewStringValue(
				fmt.Sprintf("https://%s.execute-api.us-east-1.amazonaws.com", name))
		}
		if _, ok := fields["executionArn"]; !ok {
			fields["executionArn"] = structpb.NewStringValue(
				fmt.Sprintf("arn:aws:execute-api:us-east-1:123456789012:%s", name))
		}

	case strings.Contains(resourceType, "iam") && strings.Contains(resourceType, "Role"):
		if _, ok := fields["uniqueId"]; !ok {
			fields["uniqueId"] = structpb.NewStringValue("AROA" + strings.ToUpper(name))
		}

	case strings.Contains(resourceType, "ec2") && strings.Contains(resourceType, "Instance"):
		if _, ok := fields["publicIp"]; !ok {
			fields["publicIp"] = structpb.NewStringValue("203.0.113.1")
		}
		if _, ok := fields["privateIp"]; !ok {
			fields["privateIp"] = structpb.NewStringValue("10.0.0.1")
		}
		if _, ok := fields["publicDns"]; !ok {
			fields["publicDns"] = structpb.NewStringValue(fmt.Sprintf("ec2-%s.compute-1.amazonaws.com", name))
		}

	case strings.Contains(resourceType, "lb") || strings.Contains(resourceType, "LoadBalancer"):
		if _, ok := fields["dnsName"]; !ok {
			fields["dnsName"] = structpb.NewStringValue(
				fmt.Sprintf("%s-123456.us-east-1.elb.amazonaws.com", name))
		}
		if _, ok := fields["zoneId"]; !ok {
			fields["zoneId"] = structpb.NewStringValue("Z35SXDOTRQ7X7K")
		}

	case strings.Contains(resourceType, "s3") && strings.Contains(resourceType, "Bucket"):
		if _, ok := fields["bucket"]; !ok {
			fields["bucket"] = structpb.NewStringValue(name)
		}
		if _, ok := fields["bucketDomainName"]; !ok {
			fields["bucketDomainName"] = structpb.NewStringValue(
				fmt.Sprintf("%s.s3.amazonaws.com", name))
		}
		if _, ok := fields["bucketRegionalDomainName"]; !ok {
			fields["bucketRegionalDomainName"] = structpb.NewStringValue(
				fmt.Sprintf("%s.s3.us-east-1.amazonaws.com", name))
		}

	case strings.Contains(resourceType, "rds"):
		if _, ok := fields["endpoint"]; !ok {
			fields["endpoint"] = structpb.NewStringValue(
				fmt.Sprintf("%s.mock123.us-east-1.rds.amazonaws.com:5432", name))
		}
		if _, ok := fields["address"]; !ok {
			fields["address"] = structpb.NewStringValue(
				fmt.Sprintf("%s.mock123.us-east-1.rds.amazonaws.com", name))
		}

	case strings.Contains(resourceType, "sqs") && strings.Contains(resourceType, "Queue"):
		if _, ok := fields["url"]; !ok {
			fields["url"] = structpb.NewStringValue(
				fmt.Sprintf("https://sqs.us-east-1.amazonaws.com/123456789012/%s", name))
		}

	case strings.Contains(resourceType, "sns") && strings.Contains(resourceType, "Topic"):
		// arn already injected above

	case strings.Contains(resourceType, "dynamodb") && strings.Contains(resourceType, "Table"):
		if _, ok := fields["streamArn"]; !ok {
			fields["streamArn"] = structpb.NewStringValue(mockARN + "/stream/mock")
		}
	}

	// Also inject a generic "name" if not present (many resources need it).
	if _, ok := fields["name"]; !ok {
		fields["name"] = structpb.NewStringValue(name)
	}

	return &structpb.Struct{Fields: fields}
}

// mustNewStruct wraps structpb.NewStruct and logs any conversion error.
// This catches silent failures where NewStruct returns nil (e.g. unsupported
// value types), which would cause the Python SDK to see None for all fields.
func mustNewStruct(fields map[string]any) *structpb.Struct {
	s, err := structpb.NewStruct(fields)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mock] ERROR: structpb.NewStruct failed: %v (fields: %v)\n", err, fields)
	}
	return s
}

func propertyString(v resource.PropertyValue) string {
	if !v.IsString() {
		return ""
	}
	return v.StringValue()
}

func providerReference(urn, id string) string {
	return urn + "::" + id
}

func (c *ResourceCollector) lookupProviderRegionLocked(provider string, providers map[string]string) string {
	if provider != "" {
		if region := c.providerRegions[provider]; region != "" {
			return region
		}
	}
	if len(providers) > 0 {
		// Try each provider reference.
		for _, ref := range providers {
			if region := c.providerRegions[ref]; region != "" {
				return region
			}
		}
	}

	// Fall back to stack config for all known provider region keys.
	regionKeys := []string{
		"aws:region", "aws:config:region",
		"azure-native:location", "azure:location",
		"gcp:region", "gcp:zone",
		"oci:region",
	}
	for _, key := range regionKeys {
		if v := c.stackConfig[key]; v != "" {
			return v
		}
	}

	return ""
}

// Call handles method calls on resources — return empty.
func (c *ResourceCollector) Call(_ context.Context, req *pulumirpc.ResourceCallRequest) (*pulumirpc.CallResponse, error) {
	fmt.Fprintf(os.Stderr, "[mock] Call token=%s\n", req.GetTok())
	return &pulumirpc.CallResponse{Return: req.GetArgs()}, nil
}

// ReadResource — return empty state, we don't need existing resource state.
func (c *ResourceCollector) ReadResource(_ context.Context, req *pulumirpc.ReadResourceRequest) (*pulumirpc.ReadResourceResponse, error) {
	urn := fmt.Sprintf("urn:pulumi:stack::project::%s::%s", req.GetType(), req.GetName())
	return &pulumirpc.ReadResourceResponse{
		Urn:        urn,
		Properties: req.GetProperties(),
	}, nil
}

// SignalAndWaitForShutdown is called by the SDK when the user program finishes.
func (c *ResourceCollector) SignalAndWaitForShutdown(_ context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return &emptypb.Empty{}, nil
}

// RegisterPackage — return a dummy ref.
func (c *ResourceCollector) RegisterPackage(_ context.Context, req *pulumirpc.RegisterPackageRequest) (*pulumirpc.RegisterPackageResponse, error) {
	return &pulumirpc.RegisterPackageResponse{Ref: req.GetName() + "-ref"}, nil
}

// Wait blocks until SignalAndWaitForShutdown is called by the user program.
func (c *ResourceCollector) Wait() {
	<-c.done
}

// mockEngineServer handles Engine gRPC calls (logging, root resource).
type mockEngineServer struct {
	pulumirpc.UnimplementedEngineServer
	rootURN string

	mu        sync.Mutex
	errorLogs []string // captured ERROR-level log messages
}

func (e *mockEngineServer) Log(_ context.Context, req *pulumirpc.LogRequest) (*emptypb.Empty, error) {
	if req.GetSeverity() >= pulumirpc.LogSeverity_WARNING {
		fmt.Printf("[pulumi %s] %s\n", req.GetSeverity(), req.GetMessage())
	}
	if req.GetSeverity() >= pulumirpc.LogSeverity_ERROR {
		e.mu.Lock()
		e.errorLogs = append(e.errorLogs, req.GetMessage())
		e.mu.Unlock()
	}
	return &emptypb.Empty{}, nil
}

// ErrorLogs returns all captured ERROR-level log messages.
func (e *mockEngineServer) ErrorLogs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.errorLogs))
	copy(out, e.errorLogs)
	return out
}

// ClearErrorLogs resets the captured error logs (used between retries).
func (e *mockEngineServer) ClearErrorLogs() {
	e.mu.Lock()
	e.errorLogs = nil
	e.mu.Unlock()
}

func (e *mockEngineServer) GetRootResource(_ context.Context, _ *pulumirpc.GetRootResourceRequest) (*pulumirpc.GetRootResourceResponse, error) {
	return &pulumirpc.GetRootResourceResponse{Urn: e.rootURN}, nil
}

func (e *mockEngineServer) SetRootResource(_ context.Context, req *pulumirpc.SetRootResourceRequest) (*pulumirpc.SetRootResourceResponse, error) {
	e.rootURN = req.GetUrn()
	return &pulumirpc.SetRootResourceResponse{}, nil
}

// MockGRPCServer starts both ResourceMonitor and Engine gRPC servers on
// random localhost ports and returns their addresses.
type MockGRPCServer struct {
	Collector   *ResourceCollector
	Engine      *mockEngineServer
	MonitorAddr string
	EngineAddr  string
	grpcMonitor *grpc.Server
	grpcEngine  *grpc.Server
}

func StartMockGRPCServer() (*MockGRPCServer, error) {
	collector := NewResourceCollector()
	engine := &mockEngineServer{}

	// Start ResourceMonitor server
	monLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to listen for monitor: %w", err)
	}
	monServer := grpc.NewServer()
	pulumirpc.RegisterResourceMonitorServer(monServer, collector)
	go monServer.Serve(monLis)

	// Start Engine server
	engLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		monServer.Stop()
		return nil, fmt.Errorf("failed to listen for engine: %w", err)
	}
	engServer := grpc.NewServer()
	pulumirpc.RegisterEngineServer(engServer, engine)
	go engServer.Serve(engLis)

	return &MockGRPCServer{
		Collector:   collector,
		Engine:      engine,
		MonitorAddr: monLis.Addr().String(),
		EngineAddr:  engLis.Addr().String(),
		grpcMonitor: monServer,
		grpcEngine:  engServer,
	}, nil
}

func (s *MockGRPCServer) Stop() {
	s.grpcMonitor.Stop()
	s.grpcEngine.Stop()
}

// protoToPropertyMap converts a protobuf Struct to a resource.PropertyMap.
func protoToPropertyMap(s *structpb.Struct) resource.PropertyMap {
	if s == nil {
		return resource.PropertyMap{}
	}
	pm := resource.PropertyMap{}
	for k, v := range s.GetFields() {
		pm[resource.PropertyKey(k)] = protoValueToProperty(v)
	}
	return pm
}

func protoValueToProperty(v *structpb.Value) resource.PropertyValue {
	if v == nil {
		return resource.NewNullProperty()
	}
	switch x := v.Kind.(type) {
	case *structpb.Value_StringValue:
		return resource.NewStringProperty(x.StringValue)
	case *structpb.Value_NumberValue:
		return resource.NewNumberProperty(x.NumberValue)
	case *structpb.Value_BoolValue:
		return resource.NewBoolProperty(x.BoolValue)
	case *structpb.Value_NullValue:
		return resource.NewNullProperty()
	case *structpb.Value_ListValue:
		var items []resource.PropertyValue
		for _, item := range x.ListValue.GetValues() {
			items = append(items, protoValueToProperty(item))
		}
		return resource.NewArrayProperty(items)
	case *structpb.Value_StructValue:
		return resource.NewObjectProperty(protoToPropertyMap(x.StructValue))
	default:
		return resource.NewNullProperty()
	}
}
