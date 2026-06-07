package aws

import (
	"strconv"
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/shopspring/decimal"
)

const (
	dynamoServiceCode = "AmazonDynamoDB"
	dynamoService     = "DynamoDB"
)

// ddbEntry is a helper to build a DecodedResource for DynamoDB pricing queries.
func ddbEntry(record resources.ResourceRecord, region, inputsJSON, subLabel, productFamily string, attrs, props map[string]string) resources.DecodedResource {
	a := map[string]string{"productFamily": productFamily, "servicecode": dynamoServiceCode}
	for k, v := range attrs {
		a[k] = v
	}
	return resources.DecodedResource{
		Provider:   "aws",
		Region:     region,
		Service:    dynamoService,
		Name:       record.Name,
		SubLabel:   subLabel,
		RawType:    record.Type,
		Attrs:      a,
		Props:      props,
		InputsJSON: inputsJSON,
	}
}

// tableClass returns "STANDARD" or "STANDARD_IA" from inputs, defaulting to STANDARD.
func tableClass(record resources.ResourceRecord) string {
	tc := strings.ToUpper(ExtractInput(record.Inputs, "tableClass"))
	if tc == "STANDARD_IA" {
		return "STANDARD_IA"
	}
	return "STANDARD"
}

// provisionedReadCapacity returns the readCapacity value from inputs, defaulting to 1.
func provisionedReadCapacity(record resources.ResourceRecord) decimal.Decimal {
	if v := ExtractInput(record.Inputs, "readCapacity"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return decimal.NewFromFloat(f)
		}
	}
	return decimal.NewFromInt(1)
}

// provisionedWriteCapacity returns the writeCapacity value from inputs, defaulting to 1.
func provisionedWriteCapacity(record resources.ResourceRecord) decimal.Decimal {
	if v := ExtractInput(record.Inputs, "writeCapacity"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return decimal.NewFromFloat(f)
		}
	}
	return decimal.NewFromInt(1)
}

// billingMode returns "PROVISIONED" or "PAY_PER_REQUEST" from inputs, defaulting to PROVISIONED.
func billingMode(record resources.ResourceRecord) string {
	bm := strings.ToUpper(ExtractInput(record.Inputs, "billingMode"))
	if bm == "PAY_PER_REQUEST" {
		return "PAY_PER_REQUEST"
	}
	return "PROVISIONED"
}

// throughputQueries returns Read + Write capacity pricing queries for a table or GSI.
// The group suffix and operation depend on billingMode and tableClass.
func throughputQueries(record resources.ResourceRecord, region, inputsJSON string, isPAY, isIA bool, props map[string]string) []resources.DecodedResource {
	iaSuffix := ""
	if isIA {
		iaSuffix = "IA"
	}

	var readGroup, writeGroup, operation, productFamily string
	if isPAY {
		readGroup = "DDB-ReadUnits" + iaSuffix
		writeGroup = "DDB-WriteUnits" + iaSuffix
		operation = "PayPerRequestThroughput"
		productFamily = "Amazon DynamoDB PayPerRequest Throughput"
	} else {
		readGroup = "DDB-ReadUnits" + iaSuffix
		writeGroup = "DDB-WriteUnits" + iaSuffix
		operation = "CommittedThroughput"
		productFamily = "Provisioned IOPS"
	}

	read := ddbEntry(record, region, inputsJSON, "Read Capacity", productFamily,
		map[string]string{"group": readGroup, "operation": operation}, props)
	write := ddbEntry(record, region, inputsJSON, "Write Capacity", productFamily,
		map[string]string{"group": writeGroup, "operation": operation}, props)

	if !isPAY {
		read.HourlyQty = provisionedReadCapacity(record)
		write.HourlyQty = provisionedWriteCapacity(record)
	}

	return []resources.DecodedResource{read, write}
}

// extractReplicas returns the list of regionName values from the replicas[] array input.
func extractReplicas(record resources.ResourceRecord) []string {
	val, ok := record.Inputs[resource.PropertyKey("replicas")]
	if !ok || !val.IsArray() {
		return nil
	}
	var regions []string
	for _, item := range val.ArrayValue() {
		if !item.IsObject() {
			continue
		}
		if rn, ok := item.ObjectValue()[resource.PropertyKey("regionName")]; ok && rn.IsString() {
			if r := strings.TrimSpace(rn.StringValue()); r != "" {
				regions = append(regions, r)
			}
		}
	}
	return regions
}

// DecodeTable splits a DynamoDB Table into multiple pricing queries based on
// billingMode, tableClass, and optional features (PITR, streams, replicas, etc.).
func DecodeTable(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	isIA := tableClass(record) == "STANDARD_IA"
	isPAY := billingMode(record) == "PAY_PER_REQUEST"

	props := map[string]string{
		"type":        record.Type,
		"billingMode": billingMode(record),
		"tableClass":  tableClass(record),
	}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}
	if !isPAY {
		if v := ExtractInput(record.Inputs, "readCapacity"); v != "" {
			props["readCapacity"] = v
		}
		if v := ExtractInput(record.Inputs, "writeCapacity"); v != "" {
			props["writeCapacity"] = v
		}
	}

	var results []resources.DecodedResource

	// 1. Read + Write capacity (always).
	results = append(results, throughputQueries(record, region, inputsJSON, isPAY, isIA, props)...)

	// 2. Storage (always).
	storageVolumeType := "Amazon DynamoDB - Indexed DataStore"
	if isIA {
		storageVolumeType = "Amazon DynamoDB - Indexed DataStore - IA"
	}
	results = append(results, ddbEntry(record, region, inputsJSON, "Storage", "Database Storage",
		map[string]string{"volumeType": storageVolumeType}, props))

	// 3. Point-In-Time Recovery backup storage.
	if strings.EqualFold(ExtractInput(record.Inputs, "pointInTimeRecovery.enabled"), "true") {
		results = append(results, ddbEntry(record, region, inputsJSON, "PITR Backup", "Database Storage",
			map[string]string{"volumeType": "Amazon DynamoDB - Point-In-Time-Restore (PITR) Backup Storage"}, props))
	}

	// 4. DynamoDB Streams read requests.
	if strings.EqualFold(ExtractInput(record.Inputs, "streamEnabled"), "true") {
		results = append(results, ddbEntry(record, region, inputsJSON, "Stream Reads", "API Request",
			map[string]string{"group": "DDB-StreamsReadRequests", "usagetype": "Streams-Requests", "operation": "GetRecords"}, props))
	}

	// 5. Global table replicated writes — one query per replica region.
	replicaRegions := extractReplicas(record)
	for _, replicaRegion := range replicaRegions {
		replicaWriteGroup := "DDB-ReplicatedWriteUnits"
		if isIA {
			replicaWriteGroup = "DDB-ReplicatedWriteUnitsIA"
		}
		// PAY_PER_REQUEST replicas use DDB-ReplicatedWriteUnits (no IA variant for on-demand).
		replicaProps := map[string]string{
			"type":          record.Type,
			"billingMode":   billingMode(record),
			"tableClass":    tableClass(record),
			"replicaRegion": replicaRegion,
		}
		results = append(results, ddbEntry(record, replicaRegion, inputsJSON,
			"Replicated Write ("+replicaRegion+")", "DDB-Operation-ReplicatedWrite",
			map[string]string{"group": replicaWriteGroup, "operation": "CommittedThroughput"}, replicaProps))
	}

	// 6. Import data size.
	if _, hasImport := record.Inputs[resource.PropertyKey("importTable")]; hasImport {
		results = append(results, ddbEntry(record, region, inputsJSON, "Import", "Amazon DynamoDB Import Data Size",
			map[string]string{"volumeType": "Amazon DynamoDB - Import Size"}, props))
	}

	// 7. Backup restore data size.
	if ExtractInput(record.Inputs, "restoreBackupArn") != "" || ExtractInput(record.Inputs, "restoreSourceName") != "" {
		results = append(results, ddbEntry(record, region, inputsJSON, "Restore", "Amazon DynamoDB Restore Data Size",
			map[string]string{"volumeType": "Amazon DynamoDB - Backup Restore Size"}, props))
	}

	return results
}

// DecodeGSI decodes a DynamoDB Global Secondary Index.
// The GSI inherits billingMode from its parent table via MockedProperties["billingMode"].
// tableClass is also inherited via MockedProperties["tableClass"].
func DecodeGSI(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	// Prefer mocked parent values; fall back to the GSI's own inputs.
	bm := "PROVISIONED"
	tc := "STANDARD"
	if record.MockedProperties != nil {
		if v := record.MockedProperties["billingMode"]; v != "" {
			bm = strings.ToUpper(v)
		}
		if v := record.MockedProperties["tableClass"]; v != "" {
			tc = strings.ToUpper(v)
		}
	}
	isPAY := bm == "PAY_PER_REQUEST"
	isIA := tc == "STANDARD_IA"

	props := map[string]string{
		"type":        record.Type,
		"billingMode": bm,
		"tableClass":  tc,
	}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}
	if !isPAY {
		if v := ExtractInput(record.Inputs, "readCapacity"); v != "" {
			props["readCapacity"] = v
		}
		if v := ExtractInput(record.Inputs, "writeCapacity"); v != "" {
			props["writeCapacity"] = v
		}
	}

	return throughputQueries(record, region, inputsJSON, isPAY, isIA, props)
}

// DecodeGlobalTable decodes a DynamoDB GlobalTable (TableReplicas resource).
// Produces one replicated-write query per replica region.
func DecodeGlobalTable(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	isPAY := billingMode(record) == "PAY_PER_REQUEST"
	isIA := tableClass(record) == "STANDARD_IA"

	props := map[string]string{
		"type":        record.Type,
		"billingMode": billingMode(record),
		"tableClass":  tableClass(record),
	}

	replicaRegions := extractReplicas(record)
	if len(replicaRegions) == 0 {
		// No replicas declared — nothing to price.
		return nil
	}

	var results []resources.DecodedResource
	for _, replicaRegion := range replicaRegions {
		replicaWriteGroup := "DDB-ReplicatedWriteUnits"
		if isIA {
			replicaWriteGroup = "DDB-ReplicatedWriteUnitsIA"
		}
		// PAY_PER_REQUEST uses DDB-ReplicatedWriteUnits (on-demand replicated writes).
		if isPAY {
			replicaWriteGroup = "DDB-ReplicatedWriteUnits"
		}
		replicaProps := copyProps(props)
		replicaProps["replicaRegion"] = replicaRegion

		results = append(results, ddbEntry(record, replicaRegion, inputsJSON,
			"Replicated Write ("+replicaRegion+")", "DDB-Operation-ReplicatedWrite",
			map[string]string{"group": replicaWriteGroup, "operation": "CommittedThroughput"}, replicaProps))
	}
	return results
}

// DecodeKinesisStreamingDestination decodes a DynamoDB Kinesis Streaming Destination.
// Produces a single Kinesis data capture query.
func DecodeKinesisStreamingDestination(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "streamArn"); v != "" {
		props["streamArn"] = v
	}

	return []resources.DecodedResource{
		ddbEntry(record, region, inputsJSON, "Kinesis Data Capture", "N/A",
			map[string]string{
				"group":     "DDB-Kinesis",
				"operation": "DelegatedOperations",
				"usagetype": "ChangeDataCaptureUnits-Kinesis",
			}, props),
	}
}

// DecodeTableExport decodes a DynamoDB Table Export.
// Produces one query for full exports and one for incremental exports.
func DecodeTableExport(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	exportType := strings.ToUpper(ExtractInput(record.Inputs, "exportType"))

	props := map[string]string{"type": record.Type}
	if exportType != "" {
		props["exportType"] = exportType
	}

	switch exportType {
	case "INCREMENTAL_EXPORT":
		return []resources.DecodedResource{
			ddbEntry(record, region, inputsJSON, "Incremental Export", "Amazon DynamoDB Export Data Size",
				map[string]string{
					"volumeType": "Amazon DynamoDB - Incremental Export",
					"usagetype":  "IncrementalExportDataSize-Bytes",
				}, props),
		}
	default: // FULL_EXPORT or unset
		return []resources.DecodedResource{
			ddbEntry(record, region, inputsJSON, "Full Export", "Amazon DynamoDB Export Data Size",
				map[string]string{
					"volumeType": "Amazon DynamoDB - Export Size",
					"usagetype":  "ExportDataSize-Bytes",
				}, props),
		}
	}
}

// copyProps returns a shallow copy of a string map.
func copyProps(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
