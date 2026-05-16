package aws

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/api"
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

// InferOSFromPattern infers "linux" or "windows" from an AMI name pattern or SSM path.
// TODO: how to find other OS and software
func InferOSFromPattern(pattern string) (string, bool) {
	lower := strings.ToLower(pattern)
	windowsKeywords := []string{"windows", "win2019", "win2022", "win2016", "win2012"}
	for _, kw := range windowsKeywords {
		if strings.Contains(lower, kw) {
			return "windows", true
		}
	}
	linuxKeywords := []string{"linux", "ubuntu", "debian", "rhel", "redhat", "red-hat", "centos", "suse", "amzn", "amazon-linux", "al2023", "al2"}
	for _, kw := range linuxKeywords {
		if strings.Contains(lower, kw) {
			return "linux", true
		}
	}
	return "", false
}

// mockedOS returns the collector-inferred OS from MockedProperties ("linux" or "windows").
func mockedOS(record resources.ResourceRecord) string {
	if record.MockedProperties == nil {
		return ""
	}
	os := strings.ToLower(strings.TrimSpace(record.MockedProperties["os"]))
	switch os {
	case "linux", "windows":
		return os
	default:
		return ""
	}
}

// AddMockedOS overrides the operatingSystem attr with the collector-inferred
// value from MockedProperties. If the collector didn't resolve an OS, the
// attr is removed so the default from the mapping doesn't leak through.
func AddMockedOS(record resources.ResourceRecord, mapping api.PulumiResourceDef, attrs, props map[string]string) {
	if _, defined := mapping.Attrs["operatingSystem"]; !defined {
		return
	}
	if val := mockedOS(record); val != "" {
		attrs["operatingSystem"] = val
		props["operatingSystem"] = val
	} else {
		delete(attrs, "operatingSystem")
		delete(props, "operatingSystem")
	}
}
