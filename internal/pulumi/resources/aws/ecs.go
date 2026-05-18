package aws

import (
	"strconv"
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/shopspring/decimal"
)

const (
	ecsProvider    = "aws"
	ecsService     = "Compute"
	ecsServiceCode = "AmazonECS"
)

// ecsEntry is a helper that builds a DecodedResource for an ECS pricing query.
func ecsEntry(
	record resources.ResourceRecord,
	region, inputsJSON, subLabel, productFamily string,
	attrs, props map[string]string,
) resources.DecodedResource {
	a := map[string]string{
		"productFamily": productFamily,
		"servicecode":   ecsServiceCode,
	}
	for k, v := range attrs {
		a[k] = v
	}
	return resources.DecodedResource{
		Provider:   ecsProvider,
		Region:     region,
		Service:    ecsService,
		Name:       record.Name,
		SubLabel:   subLabel,
		RawType:    record.Type,
		Attrs:      a,
		Props:      props,
		InputsJSON: inputsJSON,
	}
}

// ---------------------------------------------------------------------------
// CapacityProvider
// ---------------------------------------------------------------------------

// DecodeECSCapacityProvider decodes an aws:ecs/capacityProvider:CapacityProvider.
//
// Two sub-types are handled:
//   - managedInstancesProvider: produces an ECSManagedInstancesUsage query
//     keyed on regionCode + instanceType extracted from the launch template's
//     allowedInstanceTypes list.
//   - autoScalingGroupProvider: the capacity provider itself does not generate
//     direct Fargate charges; the underlying EC2 instances are priced separately.
//     We emit NoPricing so the resource still appears in the output.
func DecodeECSCapacityProvider(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{
		"type":   record.Type,
		"region": region,
	}

	// managedInstancesProvider path (Fargate-managed EC2 instances).
	if _, hasMIP := record.Inputs[resource.PropertyKey("managedInstancesProvider")]; hasMIP {
		instanceType := extractMIPInstanceType(record)
		if instanceType != "" {
			props["instanceType"] = instanceType
		}

		attrs := map[string]string{
			"operation": "ECSManagedInstancesUsage",
		}
		if instanceType != "" {
			attrs["instanceType"] = instanceType
		}

		return []resources.DecodedResource{
			ecsEntry(record, region, inputsJSON, "Managed Instances", "Compute", attrs, props),
		}
	}

	// autoScalingGroupProvider path — the capacity provider wraps an ASG;
	// actual compute costs belong to the EC2 instances in that ASG.
	// Mark as NoPricing so it still surfaces in the estimate output.
	return []resources.DecodedResource{
		{
			Provider:   ecsProvider,
			Region:     region,
			Service:    ecsService,
			Name:       record.Name,
			RawType:    record.Type,
			NoPricing:  true,
			Props:      props,
			InputsJSON: inputsJSON,
		},
	}
}

// extractMIPInstanceType walks the managedInstancesProvider →
// instanceLaunchTemplate → instanceRequirements → allowedInstanceTypes[]
// path and returns the first declared instance type, or "" if not found.
func extractMIPInstanceType(record resources.ResourceRecord) string {
	mip, ok := record.Inputs[resource.PropertyKey("managedInstancesProvider")]
	if !ok || !mip.IsObject() {
		return ""
	}
	ilt, ok := mip.ObjectValue()[resource.PropertyKey("instanceLaunchTemplate")]
	if !ok || !ilt.IsObject() {
		return ""
	}
	ir, ok := ilt.ObjectValue()[resource.PropertyKey("instanceRequirements")]
	if !ok || !ir.IsObject() {
		return ""
	}
	ait, ok := ir.ObjectValue()[resource.PropertyKey("allowedInstanceTypes")]
	if !ok || !ait.IsArray() {
		return ""
	}
	items := ait.ArrayValue()
	if len(items) == 0 {
		return ""
	}
	return PropertyToString(items[0])
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// DecodeECSService decodes an aws:ecs/service:Service.
//
// Pricing depends on launchType:
//   - FARGATE: one CPU query + one memory query, plus an optional OS license
//     query for Windows workloads. CPU/memory/arch/OS are inherited from the
//     associated TaskDefinition via MockedProperties (injected by the collector).
//   - EXTERNAL: a single AddExternalInstance query.
//   - EC2 (default): the service itself has no direct pricing; costs belong to
//     the underlying EC2 instances. Emits NoPricing.
func DecodeECSService(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	launchType := strings.ToUpper(strings.TrimSpace(ExtractInput(record.Inputs, "launchType")))

	props := map[string]string{
		"type":       record.Type,
		"region":     region,
		"launchType": launchType,
	}

	// Inherit TaskDefinition attributes injected by the collector.
	taskCPU := mockedProp(record, "taskCpu")
	taskMemory := mockedProp(record, "taskMemory")
	cpuArch := normalizeCPUArch(mockedProp(record, "cpuArchitecture"))
	osFamily := strings.ToUpper(strings.TrimSpace(mockedProp(record, "osFamily")))

	if taskCPU != "" {
		props["taskCpu"] = taskCPU
	}
	if taskMemory != "" {
		props["taskMemory"] = taskMemory
	}
	if cpuArch != "" {
		props["cpuArchitecture"] = cpuArch
	}
	if osFamily != "" {
		props["osFamily"] = osFamily
	}

	switch launchType {
	case "FARGATE":
		return decodeFargateService(record, region, inputsJSON, cpuArch, osFamily, props)
	case "EXTERNAL":
		return decodeExternalService(record, region, inputsJSON, props)
	default:
		// EC2 launch type: costs belong to the underlying EC2 instances.
		return []resources.DecodedResource{
			{
				Provider:   ecsProvider,
				Region:     region,
				Service:    ecsService,
				Name:       record.Name,
				RawType:    record.Type,
				NoPricing:  true,
				Props:      props,
				InputsJSON: inputsJSON,
			},
		}
	}
}

// decodeFargateService produces pricing queries for a FARGATE-launched service.
//
// Always emits:
//   - CPU query  (cputype=perCPU)
//   - Memory query (memorytype=perGB)
//
// Additionally emits an OS License Fee query when the task runs Windows.
//
// HourlyQty is set on each entry so the estimator multiplies the unit price by
// the correct number of vCPUs (or GB) × desiredCount rather than treating
// every task as 1 vCPU / 1 GB.
func decodeFargateService(
	record resources.ResourceRecord,
	region, inputsJSON, cpuArch, osFamily string,
	props map[string]string,
) []resources.DecodedResource {
	// Default to X86_64 when the task definition does not specify an arch.
	// AWS Fargate defaults to X86_64, and the pricing API returns ARM64 prices
	// when no cpuArchitecture filter is supplied, which would undercount costs.
	if cpuArch == "" {
		cpuArch = "X86_64"
	}

	cpuAttrs := map[string]string{
		"cputype":         "perCPU",
		"cpuArchitecture": cpuArch,
	}
	memAttrs := map[string]string{
		"memorytype":      "perGB",
		"cpuArchitecture": cpuArch,
	}

	// Compute per-hour quantity multipliers from taskCpu / taskMemory / desiredCount.
	// taskCpu is in millicpu (256 = 0.25 vCPU), taskMemory is in MiB (512 = 0.5 GB).
	cpuQty := fargateVCPUs(props["taskCpu"])
	memQty := fargateMemoryGB(props["taskMemory"])
	count := fargateDesiredCount(record)

	cpuHourlyQty := cpuQty.Mul(count)
	memHourlyQty := memQty.Mul(count)

	cpuEntry := ecsEntry(record, region, inputsJSON, "CPU", "Compute", cpuAttrs, props)
	cpuEntry.HourlyQty = cpuHourlyQty
	memEntry := ecsEntry(record, region, inputsJSON, "Memory", "Compute", memAttrs, props)
	memEntry.HourlyQty = memHourlyQty

	results := []resources.DecodedResource{cpuEntry, memEntry}

	// Windows tasks incur an additional OS license fee.
	if isWindowsOSFamily(osFamily) {
		osAttrs := map[string]string{
			"operatingSystem": "Windows",
			"cputype":         "perCPU",
			"cpuArchitecture": cpuArch,
		}
		osEntry := ecsEntry(record, region, inputsJSON, "OS License Fee", "Compute", osAttrs, props)
		osEntry.HourlyQty = cpuHourlyQty
		results = append(results, osEntry)
	}

	return results
}

// fargateVCPUs converts a Fargate CPU string (millicpu, e.g. "256") to vCPUs.
// Returns 1 when the value is missing or unparseable so costs are never zero.
func fargateVCPUs(taskCPU string) decimal.Decimal {
	if taskCPU == "" {
		return decimal.NewFromInt(1)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(taskCPU), 64)
	if err != nil || v <= 0 {
		return decimal.NewFromInt(1)
	}
	return decimal.NewFromFloat(v / 1024)
}

// fargateMemoryGB converts a Fargate memory string (MiB, e.g. "512") to GB.
// Returns 1 when the value is missing or unparseable.
func fargateMemoryGB(taskMemory string) decimal.Decimal {
	if taskMemory == "" {
		return decimal.NewFromInt(1)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(taskMemory), 64)
	if err != nil || v <= 0 {
		return decimal.NewFromInt(1)
	}
	return decimal.NewFromFloat(v / 1024)
}

// fargateDesiredCount reads the desiredCount input from the ECS Service record.
// Returns 1 when not set or unparseable.
func fargateDesiredCount(record resources.ResourceRecord) decimal.Decimal {
	raw := ExtractInput(record.Inputs, "desiredCount")
	if raw == "" {
		return decimal.NewFromInt(1)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v <= 0 {
		return decimal.NewFromInt(1)
	}
	return decimal.NewFromFloat(v)
}

// decodeExternalService produces a pricing query for an EXTERNAL-launched service.
// External instances are registered from on-premises or other environments.
func decodeExternalService(
	record resources.ResourceRecord,
	region, inputsJSON string,
	props map[string]string,
) []resources.DecodedResource {
	externalInstanceType := mockedProp(record, "externalInstanceType")
	if externalInstanceType == "" {
		// Fall back to a generic external instance type when not specified.
		externalInstanceType = "external"
	}

	attrs := map[string]string{
		"operation":            "AddExternalInstance",
		"externalInstanceType": externalInstanceType,
	}

	p := copyProps(props)
	p["externalInstanceType"] = externalInstanceType

	return []resources.DecodedResource{
		ecsEntry(record, region, inputsJSON, "External Instance", "Compute", attrs, p),
	}
}

// ---------------------------------------------------------------------------
// ExpressGatewayService
// ---------------------------------------------------------------------------

// DecodeECSExpressGatewayService decodes an aws:ecs/expressGatewayService:ExpressGatewayService.
// It follows the same Fargate CPU+Memory pricing model, with cpu and memory
// read directly from the resource's own inputs rather than a TaskDefinition.
func DecodeECSExpressGatewayService(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	cpu := ExtractInput(record.Inputs, "cpu")
	memory := ExtractInput(record.Inputs, "memory")

	props := map[string]string{
		"type":   record.Type,
		"region": region,
	}
	if cpu != "" {
		props["cpu"] = cpu
	}
	if memory != "" {
		props["memory"] = memory
	}

	cpuAttrs := map[string]string{"cputype": "perCPU"}
	memAttrs := map[string]string{"memorytype": "perGB"}

	return []resources.DecodedResource{
		ecsEntry(record, region, inputsJSON, "CPU", "Compute", cpuAttrs, props),
		ecsEntry(record, region, inputsJSON, "Memory", "Compute", memAttrs, props),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mockedProp safely reads a value from MockedProperties, returning "" if absent.
func mockedProp(record resources.ResourceRecord, key string) string {
	if record.MockedProperties == nil {
		return ""
	}
	return record.MockedProperties[key]
}

// normalizeCPUArch normalises the cpuArchitecture value to the form expected
// by the pricing API (e.g. "X86_64" → "X86_64", "ARM64" → "ARM64").
// Returns "" when the input is empty or unrecognised.
func normalizeCPUArch(arch string) string {
	switch strings.ToUpper(strings.TrimSpace(arch)) {
	case "X86_64", "AMD64":
		return "X86_64"
	case "ARM64":
		return "ARM64"
	default:
		return ""
	}
}

// isWindowsOSFamily returns true when the osFamily string indicates a Windows
// container OS (e.g. "WINDOWS_SERVER_2019_FULL", "WINDOWS_SERVER_2022_CORE").
func isWindowsOSFamily(osFamily string) bool {
	return strings.HasPrefix(strings.ToUpper(osFamily), "WINDOWS")
}
