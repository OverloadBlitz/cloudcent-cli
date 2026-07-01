package pulumi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/api"
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	awsdecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/aws"
	azuredecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/azure"
)

// skipTypes are internal Pulumi resource types that are never user-visible resources.
var skipTypes = map[string]bool{
	"pulumi:pulumi:Stack":    true,
	"pulumi:providers:aws":   true,
	"pulumi:providers:azure": true,
	"pulumi:providers:gcp":   true,
	"pulumi:providers:oci":   true,
}

// azureVersionRe strips the version segment from azure-native resource types.
// e.g. "azure-native:compute/v20240301:VirtualMachine" → "azure-native:compute:VirtualMachine"
var azureVersionRe = regexp.MustCompile(`/v20\d{6}[^:]*`)

// normalizeType strips Azure version segments so versioned types match the
// unversioned entries in the metadata map.
func normalizeType(typ string) string {
	if strings.HasPrefix(typ, "azure-native:") {
		return azureVersionRe.ReplaceAllString(typ, "")
	}
	return typ
}

// DecodeAllResources decodes collected Pulumi resources using the metadata-driven
// mapping. It replaces the old per-resource decoder approach — no Go code changes
// are needed to support new resource types; just update pulumi_resource_map.json
// in the pipeline and refresh metadata.
func DecodeAllResources(records []resources.ResourceRecord, meta *api.MetadataResponse) []resources.DecodedResource {
	// Build free-type lookup set from metadata.
	freeSet := make(map[string]bool, len(meta.FreeTypes))
	for _, ft := range meta.FreeTypes {
		freeSet[ft] = true
	}

	var results []resources.DecodedResource

	for _, record := range records {
		if skipTypes[record.Type] {
			continue
		}

		normalizedType := normalizeType(record.Type)

		// Check free types first.
		if freeSet[record.Type] || freeSet[normalizedType] {
			results = append(results, awsdecode.DecodeFreeResource(record))
			continue
		}

		// Look up in pulumi_resources mapping.
		mapping, ok := meta.PulumiResources[normalizedType]
		if !ok {
			// Also try the original type (in case normalizeType changed it).
			mapping, ok = meta.PulumiResources[record.Type]
		}
		if !ok {
			// Unknown type — include as no-pricing so it still shows in output.
			results = append(results, resources.DecodedResource{
				Name:       record.Name,
				RawType:    record.Type,
				NoPricing:  true,
				Props:      awsdecode.InputsToProps(record),
				InputsJSON: awsdecode.FormatInputProperties(record.Inputs),
			})
			continue
		}

		decoded := decodeFromMapping(record, mapping)
		results = append(results, decoded...)
	}

	return consolidateS3Objects(results)
}

// s3ObjectTypes are the Pulumi resource types for individual S3 objects.
var s3ObjectTypes = map[string]bool{
	"aws:s3/bucketObject:BucketObject":     true,
	"aws:s3/bucketObjectv2:BucketObjectv2": true,
}

// consolidateS3Objects merges multiple S3 BucketObject/BucketObjectv2
// DecodedResources that share the same region, bucket, and storage volume
// type into a single "S3 Objects (xN)" entry. Each object otherwise produces
// its own "Storage" line with the same 25 GB-Mo default usage, duplicating
// the bucket's own Storage line and overstating the estimate.
func consolidateS3Objects(results []resources.DecodedResource) []resources.DecodedResource {
	type group struct {
		indices []int
		names   []string
	}
	groups := make(map[string]*group)
	var order []string

	for i, d := range results {
		if !s3ObjectTypes[d.RawType] {
			continue
		}
		key := d.Region + "|" + d.Props["bucket"] + "|" + d.Props["volumeType"]
		g, ok := groups[key]
		if !ok {
			g = &group{}
			groups[key] = g
			order = append(order, key)
		}
		g.indices = append(g.indices, i)
		g.names = append(g.names, d.Name)
	}

	const maxNames = 5
	replace := make(map[int]resources.DecodedResource)
	remove := make(map[int]bool)

	for _, key := range order {
		g := groups[key]
		if len(g.indices) < 2 {
			continue
		}

		nameList := strings.Join(g.names[:min(len(g.names), maxNames)], ", ")
		if len(g.names) > maxNames {
			nameList += fmt.Sprintf(", ...and %d more", len(g.names)-maxNames)
		}

		first := g.indices[0]
		consolidated := results[first]
		consolidated.Name = fmt.Sprintf("S3 Objects (x%d)", len(g.indices))

		props := make(map[string]string, len(consolidated.Props)+2)
		for k, v := range consolidated.Props {
			props[k] = v
		}
		props["objectCount"] = strconv.Itoa(len(g.indices))
		props["objects"] = nameList
		consolidated.Props = props

		replace[first] = consolidated
		for _, idx := range g.indices[1:] {
			remove[idx] = true
		}
	}

	if len(replace) == 0 {
		return results
	}

	out := make([]resources.DecodedResource, 0, len(results))
	for i, d := range results {
		if remove[i] {
			continue
		}
		if r, ok := replace[i]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, d)
	}
	return out
}

// decodeFromMapping uses a metadata PulumiResourceDef to extract pricing
// attributes from a Pulumi resource's inputs. Returns one or more
// DecodedResources — most resource types produce one, but some (e.g. Lambda)
// are split into multiple pricing queries.
func decodeFromMapping(record resources.ResourceRecord, mapping api.PulumiResourceDef) []resources.DecodedResource {
	attrs := make(map[string]string)
	props := map[string]string{"type": record.Type}

	for canonicalName, attrDef := range mapping.Attrs {
		val := ""

		if attrDef.Input != "" {
			val = awsdecode.ExtractInput(record.Inputs, attrDef.Input)
		}

		if val == "" && attrDef.Default != "" {
			val = attrDef.Default
		}
		if val != "" && len(attrDef.Map) > 0 {
			val = awsdecode.ApplyValueMap(val, attrDef.Map)
		}

		if val != "" {
			attrs[canonicalName] = val
			props[canonicalName] = val
		}
	}

	// Region comes from the collector's MockedProperties.
	region := ""
	if record.MockedProperties != nil {
		region = record.MockedProperties["region"]
	}

	t := normalizeType(record.Type)
	inputsJSON := awsdecode.FormatInputProperties(record.Inputs)

	// Resources that produce multiple pricing queries (1:N).
	switch t {
	case "aws:appsync/graphQLApi:GraphQLApi":
		return awsdecode.DecodeGraphQLApi(record, region, inputsJSON)
	case "aws:appsync/apiCache:ApiCache":
		return awsdecode.DecodeApiCache(record, region, inputsJSON)
	case "aws:appsync/api:Api":
		return awsdecode.DecodeAppSyncApi(record, region, inputsJSON)
	case "aws:ecs/service:Service":
		return awsdecode.DecodeECSService(record, region, inputsJSON)
	case "aws:ecs/capacityProvider:CapacityProvider":
		return awsdecode.DecodeECSCapacityProvider(record, region, inputsJSON)
	case "aws:ecs/expressGatewayService:ExpressGatewayService":
		return awsdecode.DecodeECSExpressGatewayService(record, region, inputsJSON)
	case "aws:lambda/function:Function":
		return awsdecode.DecodeLambda(record, mapping, region, props, inputsJSON)
	case "aws:cloudwatch/logGroup:LogGroup":
		return awsdecode.DecodeLogGroup(record, region, inputsJSON)
	case "aws:cloudwatch/contributorInsightRule:ContributorInsightRule":
		return awsdecode.DecodeContributorInsightRule(record, region, inputsJSON)
	case "aws:cloudwatch/internetMonitor:InternetMonitor":
		return awsdecode.DecodeInternetMonitor(record, region, inputsJSON)
	case "aws:cloudwatch/eventArchive:EventArchive":
		return awsdecode.DecodeEventArchive(record, region, inputsJSON)
	case "aws:dynamodb/table:Table":
		return awsdecode.DecodeTable(record, region, inputsJSON)
	case "aws:dynamodb/globalSecondaryIndex:GlobalSecondaryIndex":
		return awsdecode.DecodeGSI(record, region, inputsJSON)
	case "aws:dynamodb/table:GlobalTable":
		return awsdecode.DecodeGlobalTable(record, region, inputsJSON)
	case "aws:dynamodb/kinesisStreamingDestination:KinesisStreamingDestination":
		return awsdecode.DecodeKinesisStreamingDestination(record, region, inputsJSON)
	case "aws:dynamodb/tableExport:TableExport":
		return awsdecode.DecodeTableExport(record, region, inputsJSON)
	case "aws:sns/topic:Topic":
		return awsdecode.DecodeSNSTopic(record, region, inputsJSON)
	case "aws:sns/topicSubscription:TopicSubscription":
		return awsdecode.DecodeSNSTopicSubscription(record, region, inputsJSON)
	case "aws:s3/bucket:Bucket", "aws:s3/bucketV2:BucketV2":
		return awsdecode.DecodeS3Bucket(record, region, inputsJSON)
	case "aws:s3/bucketObject:BucketObject", "aws:s3/bucketObjectv2:BucketObjectv2":
		return awsdecode.DecodeS3BucketObject(record, region, inputsJSON)
	case "aws:s3/directoryBucket:DirectoryBucket":
		return awsdecode.DecodeS3DirectoryBucket(record, region, inputsJSON)
	case "aws:s3tables/tableBucket:TableBucket":
		return awsdecode.DecodeS3TableBucket(record, region, inputsJSON)
	case "aws:s3/vectorsVectorBucket:VectorsVectorBucket":
		return awsdecode.DecodeS3VectorBucket(record, region, inputsJSON)
	case "aws:ebs/volume:Volume":
		return awsdecode.DecodeEBSVolume(record, region, inputsJSON)
	case "aws:lb/loadBalancer:LoadBalancer", "aws:alb/loadBalancer:LoadBalancer":
		return awsdecode.DecodeLBLoadBalancer(record, region, inputsJSON)
	case "aws:elb/loadBalancer:LoadBalancer":
		return awsdecode.DecodeClassicELB(record, region, inputsJSON)
	case "azure-native:compute:VirtualMachine":
		return azuredecode.DecodeVirtualMachine(record, region, inputsJSON)
	case "aws:rds/instance:Instance":
		return awsdecode.DecodeRDSInstance(record, region, inputsJSON)
	case "aws:redshift/cluster:Cluster":
		return awsdecode.DecodeRedshiftCluster(record, region, inputsJSON)
	case "aws:glue/job:Job":
		return awsdecode.DecodeGlueJob(record, region, inputsJSON)
	case "aws:glue/crawler:Crawler":
		return awsdecode.DecodeGlueCrawler(record, region, inputsJSON)
	case "aws:secretsmanager/secret:Secret":
		return awsdecode.DecodeSecretsManagerSecret(record, region, inputsJSON)
	case "aws:cloudfront/distribution:Distribution":
		return awsdecode.DecodeCloudFrontDistribution(record, region, inputsJSON)
	case "aws:efs/fileSystem:FileSystem":
		return awsdecode.DecodeEFSFileSystem(record, region, inputsJSON)
	case "aws:athena/workgroup:WorkGroup":
		return awsdecode.DecodeAthenaWorkGroup(record, region, inputsJSON)
	case "aws:synthetics/canary:Canary":
		return awsdecode.DecodeCanary(record, region, inputsJSON)
	case "aws:eks/nodeGroup:NodeGroup":
		return awsdecode.DecodeEKSNodeGroup(record, region, inputsJSON)
	case "azure-native:containerinstance:ContainerGroup":
		return azuredecode.DecodeContainerGroup(record, region, inputsJSON)
	case "azure-native:documentdb:DatabaseAccount":
		return azuredecode.DecodeCosmosDBAccount(record, region, inputsJSON)
	case "azure-native:storage:StorageAccount":
		return azuredecode.DecodeStorageAccount(record, region, inputsJSON)
	}

	// Single-resource path: apply any derived attrs then return.
	addDerivedAttrs(record, mapping, attrs, props)

	return []resources.DecodedResource{{
		Provider:   mapping.Provider,
		Region:     region,
		Service:    mapping.Product,
		Name:       record.Name,
		RawType:    record.Type,
		Attrs:      attrs,
		Props:      props,
		InputsJSON: inputsJSON,
	}}
}

// addDerivedAttrs applies resource-type-specific attribute enrichment that
// cannot be expressed in the metadata mapping alone.
func addDerivedAttrs(record resources.ResourceRecord, mapping api.PulumiResourceDef, attrs, props map[string]string) {
	t := normalizeType(record.Type)

	switch t {
	case "aws:ec2/instance:Instance":
		awsdecode.AddMockedOS(record, mapping, attrs, props)
	case "aws:apigatewayv2/api:Api":
		awsdecode.AddAPIGatewayV2ProtocolType(record, attrs, props)
	case "aws:cloudwatch/metricAlarm:MetricAlarm":
		awsdecode.AddMetricAlarmType(record, attrs, props)
	case "aws:cloudwatch/logSubscriptionFilter:LogSubscriptionFilter":
		awsdecode.AddLogSubscriptionFilterDestination(record, attrs, props)
	case "aws:cloudwatch/eventRule:EventRule":
		awsdecode.AddEventRuleInvocationType(record, attrs, props)
	}
}
