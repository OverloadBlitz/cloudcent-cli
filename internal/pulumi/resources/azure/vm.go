package azure

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	awsdecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/aws"
)

// normalizeVMSize converts an Azure VM size name to the instanceType key used
// by the pricing API, mirroring the pipeline's normalization:
//
//	Standard_A1_v2  → instanceType="a1v2",  tier="standard"
//	Basic_A1        → instanceType="a1",    tier="basic"
//	Standard_D2s_v3 → instanceType="d2sv3", tier="standard"
//
// The pipeline strips the tier prefix (Standard_/Basic_), removes all
// underscores and hyphens, and lowercases the result.
func normalizeVMSize(vmSize string) (instanceType, tier string) {
	lower := strings.ToLower(strings.TrimSpace(vmSize))

	switch {
	case strings.HasPrefix(lower, "standard_"):
		tier = "standard"
		lower = strings.TrimPrefix(lower, "standard_")
	case strings.HasPrefix(lower, "basic_"):
		tier = "basic"
		lower = strings.TrimPrefix(lower, "basic_")
	}

	instanceType = strings.ReplaceAll(strings.ReplaceAll(lower, "_", ""), "-", "")
	return
}

// DecodeVirtualMachine decodes an azure-native:compute:VirtualMachine resource.
func DecodeVirtualMachine(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	vmSize := awsdecode.ExtractInput(record.Inputs, "hardwareProfile.vmSize")

	instanceType, tier := normalizeVMSize(vmSize)

	// Determine OS: presence of windowsConfiguration → windows, else linux (bare, no paid OS license).
	os := "linux"
	if osProfileVal, ok := record.Inputs["osProfile"]; ok && osProfileVal.IsObject() {
		if _, hasWinCfg := osProfileVal.ObjectValue()["windowsConfiguration"]; hasWinCfg {
			os = "windows"
		}
	}

	attrs := map[string]string{
		"os":      os,
		"os_slug": "os-only",
	}
	if instanceType != "" {
		attrs["instanceType"] = instanceType
	}
	if tier != "" {
		attrs["tier"] = tier
	}

	props := map[string]string{
		"type": record.Type,
		"os":   os,
	}
	if vmSize != "" {
		props["vmSize"] = vmSize
	}
	if instanceType != "" {
		props["instanceType"] = instanceType
	}
	if tier != "" {
		props["tier"] = tier
	}
	if region != "" {
		props["region"] = region
	}

	return []resources.DecodedResource{{
		Provider:   "azure",
		Region:     region,
		Service:    "virtual-machines",
		Name:       record.Name,
		RawType:    record.Type,
		Attrs:      attrs,
		Props:      props,
		InputsJSON: inputsJSON,
	}}
}
