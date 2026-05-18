package aws

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

const (
	appSyncServiceCode = "AWSAppSync"
	appSyncService     = "AppSync"
)

// appSyncEntry builds a DecodedResource for an AppSync pricing query.
func appSyncEntry(record resources.ResourceRecord, region, inputsJSON, subLabel string, attrs, props map[string]string) resources.DecodedResource {
	a := map[string]string{"servicecode": appSyncServiceCode}
	for k, v := range attrs {
		a[k] = v
	}
	return resources.DecodedResource{
		Provider:   "aws",
		Region:     region,
		Service:    appSyncService,
		Name:       record.Name,
		SubLabel:   subLabel,
		RawType:    record.Type,
		Attrs:      a,
		Props:      props,
		InputsJSON: inputsJSON,
	}
}

// DecodeGraphQLApi decodes an AppSync GraphQL API resource.
// Produces a single pricing query for GraphQL invocations.
func DecodeGraphQLApi(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}
	if v := ExtractInput(record.Inputs, "authenticationType"); v != "" {
		props["authenticationType"] = v
	}

	return []resources.DecodedResource{
		appSyncEntry(record, region, inputsJSON, "",
			map[string]string{
				"productFamily":    "API Calls",
				"graphqloperation": "Invocation",
				"protocol":         "HTTPS",
			},
			props),
	}
}

// cacheTypeToMemorySize maps the ApiCache instance type to the cachememorysize
// pricing attribute value used in the AWS pricing API.
func cacheTypeToMemorySize(cacheType string) string {
	switch strings.ToUpper(strings.TrimSpace(cacheType)) {
	case "SMALL", "T2_SMALL":
		return "1.55"
	case "MEDIUM", "T2_MEDIUM":
		return "3.22"
	case "LARGE", "R4_LARGE":
		return "12.3"
	case "XLARGE", "R4_XLARGE":
		return "25.05"
	case "LARGE_2X", "R4_2XLARGE":
		return "50.47"
	case "LARGE_4X", "R4_4XLARGE":
		return "101.38"
	case "LARGE_8X", "R4_8XLARGE":
		return "203.26"
	case "LARGE_12X":
		return "317.77"
	default:
		return "1.55" // smallest as safe default
	}
}

// DecodeApiCache decodes an AppSync ApiCache resource.
// The cache instance type maps to a cachememorysize pricing attribute.
func DecodeApiCache(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	cacheType := ExtractInput(record.Inputs, "type")
	memorySize := cacheTypeToMemorySize(cacheType)

	props := map[string]string{"type": record.Type}
	if cacheType != "" {
		props["cacheType"] = cacheType
		props["cachememorysize"] = memorySize
	}
	if v := ExtractInput(record.Inputs, "apiCachingBehavior"); v != "" {
		props["apiCachingBehavior"] = v
	}

	return []resources.DecodedResource{
		appSyncEntry(record, region, inputsJSON, "",
			map[string]string{
				"productFamily":   "API Calls",
				"cachememorysize": memorySize,
			},
			props),
	}
}

// DecodeAppSyncApi decodes an AppSync Event API (aws:appsync/api:Api) resource.
// The Event API produces 4 pricing dimensions:
//   - GraphQL Notifications  (realtimeoperation=Notification)
//   - Connection Duration    (realtimeoperation=Duration)
//   - Event Connection       (eventapioperation=Connection duration)
//   - Event Operation        (eventapioperation=Operation)
func DecodeAppSyncApi(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}

	return []resources.DecodedResource{
		// RealTime product family — realtimeoperation dimension
		appSyncEntry(record, region, inputsJSON, "GraphQL Notifications",
			map[string]string{
				"productFamily":     "RealTime",
				"realtimeoperation": "Notification",
				"protocol":          "MQTT_WSS",
			},
			props),
		appSyncEntry(record, region, inputsJSON, "Connection Duration",
			map[string]string{
				"productFamily":     "RealTime",
				"realtimeoperation": "Duration",
				"protocol":          "MQTT_WSS",
			},
			props),
		// RealTime product family — eventapioperation dimension
		appSyncEntry(record, region, inputsJSON, "Event Connection",
			map[string]string{
				"productFamily":     "RealTime",
				"eventapioperation": "Connection duration",
				"protocol":          "MQTT_WSS",
			},
			props),
		appSyncEntry(record, region, inputsJSON, "Event Operation",
			map[string]string{
				"productFamily":     "RealTime",
				"eventapioperation": "Operation",
				"protocol":          "MQTT_WSS",
			},
			props),
	}
}
