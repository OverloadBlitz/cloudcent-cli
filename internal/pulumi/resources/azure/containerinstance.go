package azure

import (
	"strconv"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	awsdecode "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/shopspring/decimal"
)

// DecodeContainerGroup decodes an azure-native:containerinstance:ContainerGroup resource.
// Emits one sub-resource per pricing dimension: Core Duration and Memory Duration.
func DecodeContainerGroup(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	// Sum cpu and memory across all containers in the group.
	totalCPU := 0.0
	totalMemoryGB := 0.0

	if val, ok := record.Inputs[resource.PropertyKey("containers")]; ok && val.IsArray() {
		for _, item := range val.ArrayValue() {
			if !item.IsObject() {
				continue
			}
			obj := item.ObjectValue()
			if res, ok := obj[resource.PropertyKey("resources")]; ok && res.IsObject() {
				resObj := res.ObjectValue()
				if req, ok := resObj[resource.PropertyKey("requests")]; ok && req.IsObject() {
					reqObj := req.ObjectValue()
					if cpu, ok := reqObj[resource.PropertyKey("cpu")]; ok {
						if v, err := strconv.ParseFloat(awsdecode.PropertyToString(cpu), 64); err == nil {
							totalCPU += v
						}
					}
					if mem, ok := reqObj[resource.PropertyKey("memoryInGb")]; ok {
						if v, err := strconv.ParseFloat(awsdecode.PropertyToString(mem), 64); err == nil {
							totalMemoryGB += v
						}
					}
				}
			}
		}
	}

	if totalCPU == 0 {
		totalCPU = 1.0
	}
	if totalMemoryGB == 0 {
		totalMemoryGB = 1.5
	}

	cpuQty := decimal.NewFromFloat(totalCPU)
	memQty := decimal.NewFromFloat(totalMemoryGB)

	props := map[string]string{
		"cpu":      strconv.FormatFloat(totalCPU, 'f', -1, 64),
		"memoryGb": strconv.FormatFloat(totalMemoryGB, 'f', -1, 64),
		"osType":   awsdecode.ExtractInput(record.Inputs, "osType"),
	}

	coreEntry := resources.DecodedResource{
		Provider:       "azure",
		Region:         region,
		Service:        "Container",
		Name:           record.Name,
		SubLabel:       "CPU",
		RawType:        record.Type,
		Attrs:          map[string]string{"productFamily": "container-instances", "operation": "Core Duration"},
		Props:          props,
		InputsJSON:     inputsJSON,
		HourlyQty:      cpuQty,
		HourlyQtyLabel: strconv.FormatFloat(totalCPU, 'f', -1, 64) + " core(s)",
	}

	memEntry := resources.DecodedResource{
		Provider:       "azure",
		Region:         region,
		Service:        "Container",
		Name:           record.Name,
		SubLabel:       "Memory",
		RawType:        record.Type,
		Attrs:          map[string]string{"productFamily": "container-instances", "operation": "Memory Duration"},
		Props:          props,
		InputsJSON:     inputsJSON,
		HourlyQty:      memQty,
		HourlyQtyLabel: strconv.FormatFloat(totalMemoryGB, 'f', -1, 64) + " GB",
	}

	return []resources.DecodedResource{coreEntry, memEntry}
}
