package aws

import (
	"strconv"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/shopspring/decimal"
)

// DecodeEKSNodeGroup decodes an aws:eks/nodeGroup:NodeGroup resource.
// Each node is priced as an EC2 instance (servicecode=AmazonEC2, Linux, Shared tenancy).
// HourlyQty = desiredSize (defaults to 1).
func DecodeEKSNodeGroup(rec resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	// Extract instanceTypes[0] from the instanceTypes array input.
	instanceType := ""
	if v, ok := rec.Inputs[resource.PropertyKey("instanceTypes")]; ok && v.IsArray() {
		arr := v.ArrayValue()
		if len(arr) > 0 {
			instanceType = PropertyToString(arr[0])
		}
	}
	if instanceType == "" {
		instanceType = "t3.medium"
	}

	desiredSize := decimal.NewFromInt(1)
	if v := ExtractInput(rec.Inputs, "scalingConfig.desiredSize"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			desiredSize = decimal.NewFromFloat(n)
		}
	}

	props := map[string]string{
		"instanceType": instanceType,
		"desiredSize":  desiredSize.String(),
	}

	return []resources.DecodedResource{{
		Provider:  "aws",
		Region:    region,
		Service:   "Kubernetes",
		Name:      rec.Name,
		SubLabel:  "Nodes",
		RawType:   rec.Type,
		Attrs: map[string]string{
			"servicecode":     "AmazonEC2",
			"productFamily":   "Compute Instance",
			"instanceType":    instanceType,
			"operatingSystem": "Linux",
			"tenancy":         "Shared",
			"preInstalledSw":  "NA",
			"capacitystatus":  "Used",
		},
		Props:          props,
		InputsJSON:     inputsJSON,
		HourlyQty:      desiredSize,
		HourlyQtyLabel: desiredSize.String() + " node(s)",
	}}
}
