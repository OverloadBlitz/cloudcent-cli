package aws

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

// DecodeEFSFileSystem decodes an aws:efs/fileSystem:FileSystem resource.
// Prices on a GB-Mo usage basis.
func DecodeEFSFileSystem(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	// performanceMode drives storageClass selection for standard vs One Zone.
	storageClass := "General Purpose"
	if strings.EqualFold(ExtractInput(record.Inputs, "availabilityZoneName"), "") {
		// Multi-AZ (regional) — General Purpose storage class.
		storageClass = "General Purpose"
	}
	// lifecyclePolicies can transition to IA; for pricing we model the primary storage class.
	if strings.EqualFold(ExtractInput(record.Inputs, "lifecyclePolicy.0.transitionToIa"), "AFTER_7_DAYS") ||
		strings.EqualFold(ExtractInput(record.Inputs, "lifecyclePolicy.0.transitionToIa"), "AFTER_14_DAYS") ||
		strings.EqualFold(ExtractInput(record.Inputs, "lifecyclePolicy.0.transitionToIa"), "AFTER_30_DAYS") {
		storageClass = "Infrequent Access"
	}

	props := map[string]string{
		"storageClass": storageClass,
	}
	if v := ExtractInput(record.Inputs, "performanceMode"); v != "" {
		props["performanceMode"] = v
	}

	return []resources.DecodedResource{{
		Provider:   "aws",
		Region:     region,
		Service:    "other",
		Name:       record.Name,
		RawType:    record.Type,
		Attrs:      map[string]string{"servicecode": "AmazonEFS", "productFamily": "Storage", "storageClass": storageClass},
		Props:      props,
		InputsJSON: inputsJSON,
	}}
}
