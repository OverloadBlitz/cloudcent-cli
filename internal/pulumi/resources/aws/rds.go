package aws

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

const rdsServiceCode = "AmazonRDS"

func rdsEntry(record resources.ResourceRecord, region, inputsJSON, subLabel, productFamily string, extra map[string]string, props map[string]string) resources.DecodedResource {
	a := map[string]string{
		"servicecode":   rdsServiceCode,
		"productFamily": productFamily,
	}
	for k, v := range extra {
		a[k] = v
	}
	return resources.DecodedResource{
		Provider:   "aws",
		Region:     region,
		Service:    "Database Instance",
		Name:       record.Name,
		SubLabel:   subLabel,
		RawType:    record.Type,
		Attrs:      a,
		Props:      props,
		InputsJSON: inputsJSON,
	}
}

// engineName normalizes an RDS engine string to match pricing API values.
func engineName(engine string) string {
	switch strings.ToLower(engine) {
	case "mysql":
		return "MySQL"
	case "postgres", "postgresql":
		return "PostgreSQL"
	case "mariadb":
		return "MariaDB"
	case "oracle-se2", "oracle-se2-cdb":
		return "Oracle"
	case "sqlserver-ex", "sqlserver-web", "sqlserver-se", "sqlserver-ee":
		return "SQL Server"
	case "aurora-mysql":
		return "Aurora MySQL"
	case "aurora-postgresql":
		return "Aurora PostgreSQL"
	default:
		return engine
	}
}

// volumeTypeName normalizes an RDS storageType to a pricing API value.
func volumeTypeName(storageType string) string {
	switch strings.ToLower(storageType) {
	case "gp2":
		return "General Purpose"
	case "gp3":
		return "General Purpose-GP3"
	case "io1":
		return "Provisioned IOPS"
	case "io2":
		return "Provisioned IOPS-IO2"
	case "standard", "magnetic":
		return "Magnetic"
	default:
		return storageType
	}
}

// DecodeRDSInstance splits an aws:rds/instance:Instance into a compute (hourly)
// sub-resource and a storage (GB-Mo usage-based) sub-resource.
func DecodeRDSInstance(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	engine := engineName(ExtractInput(record.Inputs, "engine"))
	instanceClass := ExtractInput(record.Inputs, "instanceClass")
	if instanceClass == "" {
		instanceClass = "db.t3.micro"
	}
	storageType := ExtractInput(record.Inputs, "storageType")
	if storageType == "" {
		storageType = "gp2"
	}
	multiAZ := strings.EqualFold(ExtractInput(record.Inputs, "multiAz"), "true")

	deploymentOption := "Single-AZ"
	if multiAZ {
		deploymentOption = "Multi-AZ"
	}

	props := map[string]string{
		"instanceClass":    instanceClass,
		"engine":           engine,
		"storageType":      storageType,
		"deploymentOption": deploymentOption,
	}

	compute := rdsEntry(record, region, inputsJSON, "Instance", "Database Instance",
		map[string]string{
			"instanceType":     instanceClass,
			"databaseEngine":   engine,
			"deploymentOption": deploymentOption,
			"licenseModel":     "No license required",
		}, props)

	storage := rdsEntry(record, region, inputsJSON, "Storage", "Database Storage",
		map[string]string{
			"volumeType":       volumeTypeName(storageType),
			"databaseEngine":   engine,
			"deploymentOption": deploymentOption,
		}, props)

	return []resources.DecodedResource{compute, storage}
}
