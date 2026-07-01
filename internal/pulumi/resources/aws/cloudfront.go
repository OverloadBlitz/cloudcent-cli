package aws

import (
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

// DecodeCloudFrontDistribution decodes an aws:cloudfront/distribution:Distribution.
// CloudFront is a global service; pricing uses region="" (empty).
// Emits two usage-based sub-resources: Data Transfer out and HTTPS Requests.
func DecodeCloudFrontDistribution(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "comment"); v != "" {
		props["comment"] = v
	}

	// CloudFront pricing is global (region="").
	dataTransfer := resources.DecodedResource{
		Provider:   "aws",
		Region:     "",
		Service:    "other",
		Name:       record.Name,
		SubLabel:   "Data Transfer",
		RawType:    record.Type,
		Attrs: map[string]string{
			"servicecode":   "AmazonCloudFront",
			"productFamily": "Data Transfer",
			"transferType":  "CloudFront Outbound",
			"fromLocation":  "United States",
		},
		Props:      props,
		InputsJSON: inputsJSON,
	}

	requests := resources.DecodedResource{
		Provider:   "aws",
		Region:     "",
		Service:    "other",
		Name:       record.Name,
		SubLabel:   "Requests",
		RawType:    record.Type,
		Attrs: map[string]string{
			"servicecode":   "AmazonCloudFront",
			"productFamily": "Request",
			"requestType":   "CloudFront-Request-Tier1",
		},
		Props:      props,
		InputsJSON: inputsJSON,
	}

	return []resources.DecodedResource{dataTransfer, requests}
}
