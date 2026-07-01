package azure

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	awsdecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/aws"
)

// DecodeStorageAccount decodes an azure-native:storage:StorageAccount resource.
// Prices the primary block blob storage tier (GB-Mo, usage-based).
func DecodeStorageAccount(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	// Determine access tier (Hot/Cool/Archive); default Hot.
	accessTier := "Hot"
	if v := awsdecode.ExtractInput(record.Inputs, "accessTier"); v != "" {
		switch strings.ToLower(v) {
		case "cool":
			accessTier = "Cool"
		case "archive":
			accessTier = "Archive"
		case "cold":
			accessTier = "Cold"
		}
	}

	// Determine redundancy from SKU name; default LRS.
	redundancy := "LRS"
	skuName := strings.ToUpper(awsdecode.ExtractInput(record.Inputs, "sku.name"))
	switch {
	case strings.Contains(skuName, "GRS"):
		redundancy = "GRS"
	case strings.Contains(skuName, "ZRS"):
		redundancy = "ZRS"
	case strings.Contains(skuName, "RAGRS"):
		redundancy = "RA-GRS"
	}

	props := map[string]string{
		"accessTier": accessTier,
		"redundancy": redundancy,
	}
	if v := awsdecode.ExtractInput(record.Inputs, "kind"); v != "" {
		props["kind"] = v
	}

	return []resources.DecodedResource{{
		Provider:   "azure",
		Region:     region,
		Service:    "Object Storage",
		Name:       record.Name,
		RawType:    record.Type,
		Attrs: map[string]string{
			"productFamily": "storage",
			"storageType":   "Block Blob",
			"accessTier":    accessTier,
			"redundancy":    redundancy,
		},
		Props:       props,
		InputsJSON:  inputsJSON,
		// Exclude zero-price data-write/retrieval operation entries;
		// we only want the per-GB-Mo capacity pricing row.
		PriceFilter: ">0",
	}}
}
