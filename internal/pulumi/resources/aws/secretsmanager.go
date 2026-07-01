package aws

import (
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

const smServiceCode = "AWSSecretsManager"

// DecodeSecretsManagerSecret decodes an aws:secretsmanager/secret:Secret resource.
// Emits two sub-resources: the per-secret monthly charge and the API request charge.
func DecodeSecretsManagerSecret(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}

	secret := resources.DecodedResource{
		Provider:   "aws",
		Region:     region,
		Service:    "other",
		Name:       record.Name,
		SubLabel:   "Secret",
		RawType:    record.Type,
		Attrs:      map[string]string{"servicecode": smServiceCode, "productFamily": "Secret", "group": "AWSSecretsManager-Secret"},
		Props:      props,
		InputsJSON: inputsJSON,
	}

	apiRequest := resources.DecodedResource{
		Provider:   "aws",
		Region:     region,
		Service:    "other",
		Name:       record.Name,
		SubLabel:   "API Requests",
		RawType:    record.Type,
		Attrs:      map[string]string{"servicecode": smServiceCode, "productFamily": "API Request", "group": "AWSSecretsManager-APIRequest"},
		Props:      props,
		InputsJSON: inputsJSON,
	}

	return []resources.DecodedResource{secret, apiRequest}
}
