package aws

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

const (
	elbServiceCode = "AWSELB"
	elbService     = "ELB"
)

// elbEntry builds a DecodedResource for an ELB pricing query.
func elbEntry(record resources.ResourceRecord, region, inputsJSON, subLabel string, attrs, props map[string]string) resources.DecodedResource {
	a := map[string]string{"servicecode": elbServiceCode}
	for k, v := range attrs {
		a[k] = v
	}
	return resources.DecodedResource{
		Provider:   "aws",
		Region:     region,
		Service:    elbService,
		Name:       record.Name,
		SubLabel:   subLabel,
		RawType:    record.Type,
		Attrs:      a,
		Props:      props,
		InputsJSON: inputsJSON,
	}
}

// lbTypeToAttrs maps a loadBalancerType value to its pricing productFamily and operation.
func lbTypeToAttrs(lbType string) (productFamily, operation string) {
	switch strings.ToLower(strings.TrimSpace(lbType)) {
	case "network":
		return "Load Balancer-Network", "LoadBalancing:Network"
	case "gateway":
		return "Load Balancer-Gateway", "LoadBalancing:Gateway"
	default: // "application" or empty
		return "Load Balancer-Application", "LoadBalancing:Application"
	}
}

// DecodeLBLoadBalancer decodes aws:lb/loadBalancer:LoadBalancer and
// aws:alb/loadBalancer:LoadBalancer into two SubLabel pricing queries:
//   - "Hourly"  — load balancer instance-hour charge
//   - "LCU"     — load balancer capacity unit charge
//
// The productFamily and operation are derived from the loadBalancerType input
// (application / network / gateway). Defaults to application when unset.
func DecodeLBLoadBalancer(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	lbType := ExtractInput(record.Inputs, "loadBalancerType")

	productFamily, operation := lbTypeToAttrs(lbType)

	props := map[string]string{
		"type":             record.Type,
		"loadBalancerType": lbType,
	}
	if lbType == "" {
		props["loadBalancerType"] = "application"
	}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}
	if v := ExtractInput(record.Inputs, "internal"); v != "" {
		props["internal"] = v
	}

	baseAttrs := map[string]string{
		"productFamily": productFamily,
		"operation":     operation,
		"group":         "ELB:Balancing",
		"locationType":  "AWS Region",
	}

	hourlyAttrs := make(map[string]string)
	for k, v := range baseAttrs {
		hourlyAttrs[k] = v
	}
	hourlyAttrs["usagetype"] = "LoadBalancerUsage"

	lcuAttrs := make(map[string]string)
	for k, v := range baseAttrs {
		lcuAttrs[k] = v
	}
	lcuAttrs["usagetype"] = "LCUUsage"

	return []resources.DecodedResource{
		elbEntry(record, region, inputsJSON, "Hourly", hourlyAttrs, props),
		elbEntry(record, region, inputsJSON, "LCU", lcuAttrs, props),
	}
}

// DecodeClassicELB decodes aws:elb/loadBalancer:LoadBalancer (Classic ELB)
// into two SubLabel pricing queries:
//   - "Hourly"        — load balancer instance-hour charge
//   - "DataProcessing" — data processed by the load balancer
func DecodeClassicELB(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}
	if v := ExtractInput(record.Inputs, "internal"); v != "" {
		props["internal"] = v
	}

	baseAttrs := map[string]string{
		"productFamily": "Load Balancer",
		"operation":     "LoadBalancing",
		"group":         "ELB:Balancing",
	}

	return []resources.DecodedResource{
		elbEntry(record, region, inputsJSON, "Hourly", baseAttrs, props),
		elbEntry(record, region, inputsJSON, "DataProcessing", baseAttrs, props),
	}
}
