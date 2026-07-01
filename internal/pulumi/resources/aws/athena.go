package aws

import (
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

// DecodeAthenaWorkGroup decodes an aws:athena/workgroup:WorkGroup resource.
// Prices on a TB-scanned usage basis.
func DecodeAthenaWorkGroup(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}

	return []resources.DecodedResource{{
		Provider:   "aws",
		Region:     region,
		Service:    "other",
		Name:       record.Name,
		SubLabel:   "Data Scanned",
		RawType:    record.Type,
		Attrs: map[string]string{
			"servicecode":   "AmazonAthena",
			"productFamily": "Athena Queries",
			"usagetype":     "DataScannedInTB",
		},
		Props:      props,
		InputsJSON: inputsJSON,
	}}
}
