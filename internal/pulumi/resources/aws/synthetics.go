package aws

import (
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

// DecodeCanary decodes an aws:synthetics/canary:Canary resource.
// AWS Synthetics pricing has no data in the current pipeline scraper;
// this decoder exists for forward-compatibility but will return no pricing match.
func DecodeCanary(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}
	runtimeVersion := ExtractInput(record.Inputs, "runtimeVersion")
	if runtimeVersion == "" {
		runtimeVersion = "syn-nodejs-puppeteer-6.2"
	}
	props["runtimeVersion"] = runtimeVersion

	return []resources.DecodedResource{{
		Provider:   "aws",
		Region:     region,
		Service:    "other",
		Name:       record.Name,
		SubLabel:   "Canary Runs",
		RawType:    record.Type,
		Attrs: map[string]string{
			"servicecode":   "AWSSyntheticsCanary",
			"productFamily": "CloudWatch Canary Run",
		},
		Props:      props,
		InputsJSON: inputsJSON,
	}}
}
