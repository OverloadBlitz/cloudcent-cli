package aws

import (
	"strconv"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	"github.com/shopspring/decimal"
)

// DecodeRedshiftCluster decodes an aws:redshift/cluster:Cluster resource.
// It prices per compute node (nodeType × numberOfNodes).
func DecodeRedshiftCluster(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	nodeType := ExtractInput(record.Inputs, "nodeType")
	if nodeType == "" {
		nodeType = "ra3.xlplus"
	}

	// Single-node clusters use 1 node; multi-node uses numberOfNodes.
	numberOfNodes := decimal.NewFromInt(1)
	if v := ExtractInput(record.Inputs, "numberOfNodes"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			numberOfNodes = decimal.NewFromFloat(n)
		}
	}

	props := map[string]string{
		"nodeType":      nodeType,
		"numberOfNodes": numberOfNodes.String(),
	}

	return []resources.DecodedResource{{
		Provider:      "aws",
		Region:        region,
		Service:       "other",
		Name:          record.Name,
		RawType:       record.Type,
		Attrs:         map[string]string{"servicecode": "AmazonRedshift", "productFamily": "Compute Instance", "instanceType": nodeType},
		Props:         props,
		InputsJSON:    inputsJSON,
		HourlyQty:     numberOfNodes,
		HourlyQtyLabel: numberOfNodes.String() + " node(s)",
	}}
}
