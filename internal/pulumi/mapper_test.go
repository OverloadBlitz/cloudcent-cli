package pulumi

import (
	"strings"
	"testing"

	"github.com/OverloadBlitz/cloudcent-cli/internal/api"
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	awsdecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
)

func TestDecodeAllResourcesIncludesNestedInputProperties(t *testing.T) {
	const resourceType = "aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2"

	records := []resources.ResourceRecord{
		{
			Type: resourceType,
			Name: "archive-policy",
			Inputs: resource.PropertyMap{
				"bucket": resource.NewStringProperty("logs-bucket"),
				"rules": resource.NewArrayProperty([]resource.PropertyValue{
					resource.NewObjectProperty(resource.PropertyMap{
						"status": resource.NewStringProperty("Enabled"),
						"transitions": resource.NewArrayProperty([]resource.PropertyValue{
							resource.NewObjectProperty(resource.PropertyMap{
								"days":         resource.NewNumberProperty(30),
								"storageClass": resource.NewStringProperty("STANDARD_IA"),
							}),
						}),
					}),
				}),
			},
		},
	}

	meta := &api.MetadataResponse{
		PulumiResources: map[string]api.PulumiResourceDef{
			resourceType: {
				Provider: "aws",
				Product:  "S3",
			},
		},
	}

	decoded := DecodeAllResources(records, meta)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 decoded resource, got %d", len(decoded))
	}

	inputs := decoded[0].InputsJSON
	for _, want := range []string{
		`"bucket": "logs-bucket"`,
		`"rules": [`,
		`"status": "Enabled"`,
		`"transitions": [`,
		`"storageClass": "STANDARD_IA"`,
	} {
		if !strings.Contains(inputs, want) {
			t.Fatalf("expected formatted inputs to contain %s, got:\n%s", want, inputs)
		}
	}
}

func TestApplyValueMap(t *testing.T) {
	tests := []struct {
		name string
		val  string
		m    map[string]string
		want string
	}{
		{"exact match", "postgres", map[string]string{"postgres": "PostgreSQL"}, "PostgreSQL"},
		{"case-insensitive", "POSTGRES", map[string]string{"postgres": "PostgreSQL"}, "PostgreSQL"},
		{"no match passthrough", "mariadb-custom", map[string]string{"postgres": "PostgreSQL"}, "mariadb-custom"},
		{"boolean true", "true", map[string]string{"true": "Multi-AZ", "false": "Single-AZ"}, "Multi-AZ"},
		{"boolean false", "false", map[string]string{"true": "Multi-AZ", "false": "Single-AZ"}, "Single-AZ"},
		{"engine redis", "redis", map[string]string{"redis": "Redis", "memcached": "Memcached"}, "Redis"},
		{"engine Redis caps", "Redis", map[string]string{"redis": "Redis", "memcached": "Memcached"}, "Redis"},
		{"empty map", "val", map[string]string{}, "val"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := awsdecode.ApplyValueMap(tt.val, tt.m)
			if got != tt.want {
				t.Errorf("applyValueMap(%q) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

func TestDecodeRDSWithValueMapping(t *testing.T) {
	records := []resources.ResourceRecord{
		{
			Type: "aws:rds/instance:Instance",
			Name: "my-postgres-db",
			Inputs: resource.PropertyMap{
				"instanceClass": resource.NewStringProperty("db.t3.medium"),
				"engine":        resource.NewStringProperty("postgres"),
				"multiAz":       resource.NewBoolProperty(true),
			},
			MockedProperties: map[string]string{"region": "us-east-1"},
		},
	}

	meta := &api.MetadataResponse{
		PulumiResources: map[string]api.PulumiResourceDef{
			"aws:rds/instance:Instance": {
				Provider: "aws",
				Product:  "RDS Instance",
				Attrs: map[string]api.PulumiAttrMapping{
					"instance_type": {Input: "instanceClass"},
					"database_engine": {Input: "engine", Map: map[string]string{
						"postgres": "PostgreSQL", "mysql": "MySQL",
					}},
					"deployment_option": {Input: "multiAz", Default: "Single-AZ", Map: map[string]string{
						"true": "Multi-AZ", "false": "Single-AZ",
					}},
					"license_model": {Default: "No License required"},
				},
			},
		},
	}

	decoded := DecodeAllResources(records, meta)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 decoded resource, got %d", len(decoded))
	}

	d := decoded[0]
	checks := map[string]string{
		"instance_type":     "db.t3.medium",
		"database_engine":   "PostgreSQL",
		"deployment_option": "Multi-AZ",
		"license_model":     "No License required",
	}
	for attr, want := range checks {
		got := d.Attrs[attr]
		if got != want {
			t.Errorf("attr %q = %q, want %q", attr, got, want)
		}
	}
	if d.Provider != "aws" {
		t.Errorf("provider = %q, want aws", d.Provider)
	}
	if d.Region != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", d.Region)
	}
}

func TestDecodeEC2OperatingSystemUsesMockedAMIInference(t *testing.T) {
	records := []resources.ResourceRecord{
		{
			Type: "aws:ec2/instance:Instance",
			Name: "web",
			Inputs: resource.PropertyMap{
				"ami":          resource.NewStringProperty("ami-mock-linux"),
				"instanceType": resource.NewStringProperty("t3.micro"),
			},
			MockedProperties: map[string]string{"os": "linux", "region": "us-west-2"},
		},
	}

	meta := &api.MetadataResponse{
		PulumiResources: map[string]api.PulumiResourceDef{
			"aws:ec2/instance:Instance": {
				Provider: "aws",
				Attrs: map[string]api.PulumiAttrMapping{
					"instance_type":   {Input: "instanceType"},
					"operatingSystem": {Input: "ami", Default: "Linux"},
					"servicecode":     {Default: "AmazonEC2"},
				},
			},
		},
	}

	decoded := DecodeAllResources(records, meta)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 decoded resource, got %d", len(decoded))
	}

	if got := decoded[0].Attrs["operatingSystem"]; got != "linux" {
		t.Fatalf("operatingSystem = %q, want linux", got)
	}
	if got := decoded[0].Attrs["instance_type"]; got != "t3.micro" {
		t.Fatalf("instance_type = %q, want t3.micro", got)
	}
}

func TestDecodeEC2OperatingSystemOmitsUnknownAMI(t *testing.T) {
	records := []resources.ResourceRecord{
		{
			Type: "aws:ec2/instance:Instance",
			Name: "web",
			Inputs: resource.PropertyMap{
				"ami":          resource.NewStringProperty("ami-1234567890abcdef0"),
				"instanceType": resource.NewStringProperty("t3.micro"),
			},
		},
	}

	meta := &api.MetadataResponse{
		PulumiResources: map[string]api.PulumiResourceDef{
			"aws:ec2/instance:Instance": {
				Provider: "aws",
				Attrs: map[string]api.PulumiAttrMapping{
					"instance_type":   {Input: "instanceType"},
					"operatingSystem": {Input: "ami", Default: "Linux"},
				},
			},
		},
	}

	decoded := DecodeAllResources(records, meta)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 decoded resource, got %d", len(decoded))
	}

	if got := decoded[0].Attrs["operatingSystem"]; got != "" {
		t.Fatalf("operatingSystem = %q, want omitted", got)
	}
}

func TestDecodeElastiCacheEngineCapitalization(t *testing.T) {
	records := []resources.ResourceRecord{
		{
			Type: "aws:elasticache/cluster:Cluster",
			Name: "my-redis",
			Inputs: resource.PropertyMap{
				"nodeType": resource.NewStringProperty("cache.t3.micro"),
				"engine":   resource.NewStringProperty("redis"),
			},
		},
	}

	meta := &api.MetadataResponse{
		PulumiResources: map[string]api.PulumiResourceDef{
			"aws:elasticache/cluster:Cluster": {
				Provider: "aws",
				Product:  "Elastic Cache Instance",
				Attrs: map[string]api.PulumiAttrMapping{
					"instance_type": {Input: "nodeType"},
					"cache_engine": {Input: "engine", Default: "Redis", Map: map[string]string{
						"redis": "Redis", "memcached": "Memcached",
					}},
					"current_generation": {Default: "Yes"},
				},
			},
		},
	}

	decoded := DecodeAllResources(records, meta)
	if len(decoded) != 1 {
		t.Fatalf("expected 1, got %d", len(decoded))
	}

	d := decoded[0]
	if d.Attrs["cache_engine"] != "Redis" {
		t.Errorf("cache_engine = %q, want Redis", d.Attrs["cache_engine"])
	}
	if d.Attrs["current_generation"] != "Yes" {
		t.Errorf("current_generation = %q, want Yes", d.Attrs["current_generation"])
	}
}

func TestInferOSFromPatternOnlyReturnsKnownMatches(t *testing.T) {
	tests := []struct {
		pattern string
		wantOS  string
		wantOK  bool
	}{
		{pattern: "Windows_Server-2022-English-Full-Base-*", wantOS: "windows", wantOK: true},
		{pattern: "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64", wantOS: "linux", wantOK: true},
		{pattern: "ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*", wantOS: "linux", wantOK: true},
		{pattern: "my-custom-image-*", wantOS: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			gotOS, gotOK := awsdecode.InferOSFromPattern(tt.pattern)
			if gotOS != tt.wantOS || gotOK != tt.wantOK {
				t.Fatalf("inferOSFromPattern(%q) = (%q, %v), want (%q, %v)", tt.pattern, gotOS, gotOK, tt.wantOS, tt.wantOK)
			}
		})
	}
}

func TestDecodeECSFargate(t *testing.T) {
	records := []resources.ResourceRecord{
		{
			Type: "aws:ecs/taskDefinition:TaskDefinition",
			Name: "my-task",
			Inputs: resource.PropertyMap{
				"cpu":    resource.NewStringProperty("256"),
				"memory": resource.NewStringProperty("512"),
			},
		},
	}

	meta := &api.MetadataResponse{
		PulumiResources: map[string]api.PulumiResourceDef{
			"aws:ecs/taskDefinition:TaskDefinition": {
				Provider: "aws",
				Product:  "ECS",
				Attrs: map[string]api.PulumiAttrMapping{
					"instance_type": {Input: "cpu"},
					"operation":     {Input: "memory"},
				},
			},
		},
	}

	decoded := DecodeAllResources(records, meta)
	if len(decoded) != 1 {
		t.Fatalf("expected 1, got %d", len(decoded))
	}

	d := decoded[0]
	if d.Attrs["instance_type"] != "256" {
		t.Errorf("instance_type (cpu) = %q, want 256", d.Attrs["instance_type"])
	}
	if d.Attrs["operation"] != "512" {
		t.Errorf("operation (memory) = %q, want 512", d.Attrs["operation"])
	}
}

func TestDecodeAPIGatewayV2APIAddsProtocolTypePricingAttr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "http", input: "HTTP", expected: "HTTP"},
		{name: "websocket", input: "WEBSOCKET", expected: "WEBSOCKET"},
		{name: "normalizes case", input: "websocket", expected: "WEBSOCKET"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := []resources.ResourceRecord{
				{
					Type: "aws:apigatewayv2/api:Api",
					Name: "api",
					Inputs: resource.PropertyMap{
						"protocolType": resource.NewStringProperty(tt.input),
					},
				},
			}

			meta := &api.MetadataResponse{
				PulumiResources: map[string]api.PulumiResourceDef{
					"aws:apigatewayv2/api:Api": {
						Provider: "aws",
						Attrs: map[string]api.PulumiAttrMapping{
							"product_family": {Default: "API Gateway V2"},
							"servicecode":    {Default: "AmazonApiGateway"},
						},
					},
				},
			}

			decoded := DecodeAllResources(records, meta)
			if len(decoded) != 1 {
				t.Fatalf("expected 1 decoded resource, got %d", len(decoded))
			}

			// protocol_type must NOT be in attrs (it's not an AWS pricing attribute
			// and would cause pricing lookups to return no results).
			if got := decoded[0].Attrs["protocol_type"]; got != "" {
				t.Fatalf("protocol_type should not be in attrs (breaks pricing query), got %q", got)
			}
			// protocol_type must be in props (for display purposes only).
			if got := decoded[0].Props["protocol_type"]; got != tt.expected {
				t.Fatalf("props protocol_type = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestDecodeAPIGatewayV2APIPreservesMappedProtocolTypeAttr(t *testing.T) {
	records := []resources.ResourceRecord{
		{
			Type: "aws:apigatewayv2/api:Api",
			Name: "api",
			Inputs: resource.PropertyMap{
				"protocolType": resource.NewStringProperty("HTTP"),
			},
		},
	}

	meta := &api.MetadataResponse{
		PulumiResources: map[string]api.PulumiResourceDef{
			"aws:apigatewayv2/api:Api": {
				Provider: "aws",
				Attrs: map[string]api.PulumiAttrMapping{
					"protocolType": {Input: "protocolType"},
				},
			},
		},
	}

	decoded := DecodeAllResources(records, meta)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 decoded resource, got %d", len(decoded))
	}

	if got := decoded[0].Attrs["protocolType"]; got != "HTTP" {
		t.Fatalf("protocolType = %q, want HTTP", got)
	}
	if _, ok := decoded[0].Attrs["protocol_type"]; ok {
		t.Fatalf("expected derived protocol_type not to duplicate metadata-mapped protocolType attr")
	}
}
