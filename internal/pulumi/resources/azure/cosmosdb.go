package azure

import (
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	awsdecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/aws"
)

// DecodeCosmosDBAccount decodes an azure-native:documentdb:DatabaseAccount resource.
// Prices the base provisioned throughput (ProvisionedRU, Single Write, 100 RU/s block).
func DecodeCosmosDBAccount(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := awsdecode.ExtractInput(record.Inputs, "databaseAccountOfferType"); v != "" {
		props["offerType"] = v
	}

	return []resources.DecodedResource{{
		Provider:   "azure",
		Region:     region,
		Service:    "Database Instance",
		Name:       record.Name,
		RawType:    record.Type,
		Attrs: map[string]string{
			"productFamily":    "cosmos-db",
			"operation":        "ProvisionedRU",
			"deploymentOption": "Single Write",
			"services":         "single",
		},
		Props:      props,
		InputsJSON: inputsJSON,
	}}
}
