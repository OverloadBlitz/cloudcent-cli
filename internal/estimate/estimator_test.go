package estimate

import (
	"encoding/json"
	"testing"

	"github.com/OverloadBlitz/cloudcent-cli/internal/api"
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

type stubBatchPricingClient struct {
	lastRequest api.BatchPricingRequest
	response    *api.BatchPricingApiResponse
	err         error
}

func (s *stubBatchPricingClient) FetchPricingBatch(req api.BatchPricingRequest) (*api.BatchPricingApiResponse, error) {
	s.lastRequest = req
	return s.response, s.err
}

func TestEstimateAllResourcesMatchesPricingItemsByResourceFields(t *testing.T) {
	resp := api.BatchPricingApiResponse{
		"opaque-response-key": {
			{
				Product:  "ec2",
				Provider: "aws",
				Region:   "us-west-2",
				Attributes: map[string]*api.AttrValue{
					"instanceType":  mustAttrValue(t, `"t3.micro"`),
					"os_app_bundle": mustAttrValue(t, `"linux"`),
				},
				Prices: []api.Price{
					{
						PricingModel: stringPtr("OnDemand"),
						Unit:         stringPtr("Hrs"),
						Rates: []api.PriceRate{
							{Price: mustAttrValue(t, `"0.0104"`)},
						},
					},
				},
				MinPrice: mustAttrValue(t, `"0.0104"`),
				MaxPrice: mustAttrValue(t, `"0.0104"`),
			},
		},
	}
	client := &stubBatchPricingClient{response: &resp}

	results, err := EstimateAllResources(client, []resources.DecodedResource{
		{
			Provider: "aws",
			Region:   "us-west-2",
			Service:  "ec2",
			Name:     "web-server",
			Attrs: map[string]string{
				"instanceType":  "t3.micro",
				"os_app_bundle": "linux",
				"tenancy":       "",
			},
			PriceFilter: ">=0.2",
		},
	}, nil, nil)
	if err != nil {
		t.Fatalf("EstimateAllResources returned error: %v", err)
	}

	if len(client.lastRequest.Requests) != 1 {
		t.Fatalf("expected 1 batch request, got %d", len(client.lastRequest.Requests))
	}

	request := client.lastRequest.Requests[0]
	if request.Product != "ec2" {
		t.Fatalf("expected product ec2 in batch request, got %q", request.Product)
	}
	if request.Region != "us-west-2" {
		t.Fatalf("expected region us-west-2 in batch request, got %q", request.Region)
	}
	if _, ok := request.Attrs["tenancy"]; ok {
		t.Fatalf("expected empty attrs to be omitted from batch request")
	}
	if got := request.Attrs["os_app_bundle"]; got != "linux" {
		t.Fatalf("expected os_app_bundle attr to be linux, got %q", got)
	}
	if got := request.Price; got != ">=0.2" {
		t.Fatalf("expected price filter >=0.2 in batch request, got %q", got)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 estimate result, got %d", len(results))
	}

	result := results[0]
	if result.ResourceName != "web-server" {
		t.Fatalf("expected resource name web-server, got %q", result.ResourceName)
	}
	if result.Product != "aws ec2" {
		t.Fatalf("expected display product aws ec2, got %q", result.Product)
	}
	if len(result.Prices) == 0 {
		t.Fatalf("expected at least one price entry")
	}
	if result.Prices[0].Model != "OnDemand" {
		t.Fatalf("expected first price model OnDemand, got %q", result.Prices[0].Model)
	}
	if result.Prices[0].RatePerHr.String() != "0.0104" {
		t.Fatalf("expected OnDemand rate 0.0104, got %s", result.Prices[0].RatePerHr.String())
	}
	if !result.Prices[0].IsCurrent {
		t.Fatalf("expected OnDemand price to be marked as current")
	}
}

func TestEstimateAllResourcesMatchesPricingItemsIgnoringCase(t *testing.T) {
	resp := api.BatchPricingApiResponse{
		"mixed-case-response": {
			{
				Product:  "EC2",
				Provider: "AWS",
				Region:   "US-EAST-2",
				Attributes: map[string]*api.AttrValue{
					"INSTANCE_TYPE": mustAttrValue(t, `"t2.micro"`),
					"OS_APP_BUNDLE": mustAttrValue(t, `"Linux"`),
					"TENANCY":       mustAttrValue(t, `"shared"`),
				},
				Prices: []api.Price{
					{
						PricingModel: stringPtr("OnDemand"),
						Unit:         stringPtr("Hrs"),
						Rates: []api.PriceRate{
							{Price: mustAttrValue(t, `"0.0116"`)},
						},
					},
				},
			},
		},
	}
	client := &stubBatchPricingClient{response: &resp}

	results, err := EstimateAllResources(client, []resources.DecodedResource{
		{
			Provider: "aws",
			Region:   "us-east-2",
			Service:  "ec2",
			Name:     "web-server-www",
			Attrs: map[string]string{
				"instance_type": "t2.micro",
				"os_app_bundle": "linux",
				"tenancy":       "Shared",
			},
		},
	}, nil, nil)
	if err != nil {
		t.Fatalf("EstimateAllResources returned error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 estimate result, got %d", len(results))
	}

	if results[0].OnDemandRate.String() != "0.0116" {
		t.Fatalf("expected mixed-case response to match OnDemand rate 0.0116, got %s", results[0].OnDemandRate.String())
	}
}

func TestEstimateAllResourcesReusesSamePricingItemForDuplicateResources(t *testing.T) {
	resp := api.BatchPricingApiResponse{
		"shared-price": {
			{
				Product:  "ec2",
				Provider: "aws",
				Attributes: map[string]*api.AttrValue{
					"instanceType":  mustAttrValue(t, `"t3.micro"`),
					"os_app_bundle": mustAttrValue(t, `"linux"`),
				},
				Prices: []api.Price{
					{
						PricingModel: stringPtr("OnDemand"),
						Unit:         stringPtr("Hrs"),
						Rates: []api.PriceRate{
							{Price: mustAttrValue(t, `"0.0104"`)},
						},
					},
				},
			},
		},
	}
	client := &stubBatchPricingClient{response: &resp}

	results, err := EstimateAllResources(client, []resources.DecodedResource{
		{
			Provider: "aws",
			Service:  "ec2",
			Name:     "web-1",
			Attrs: map[string]string{
				"instanceType":  "t3.micro",
				"os_app_bundle": "linux",
			},
		},
		{
			Provider: "aws",
			Service:  "ec2",
			Name:     "web-2",
			Attrs: map[string]string{
				"instanceType":  "t3.micro",
				"os_app_bundle": "linux",
			},
		},
	}, nil, nil)
	if err != nil {
		t.Fatalf("EstimateAllResources returned error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 estimate results, got %d", len(results))
	}

	for _, result := range results {
		if result.OnDemandRate.String() != "0.0104" {
			t.Fatalf("expected duplicated resources to share the same OnDemand rate 0.0104, got %s", result.OnDemandRate.String())
		}
	}
}

func mustAttrValue(t *testing.T, raw string) *api.AttrValue {
	t.Helper()

	var value api.AttrValue
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("failed to build AttrValue from %s: %v", raw, err)
	}

	return &value
}

func stringPtr(v string) *string {
	return &v
}

func TestResolveUsageQty_ServiceSubLabelDefault(t *testing.T) {
	tests := []struct {
		name     string
		service  string
		subLabel string
		wantQty  float64
	}{
		{"dynamodb storage", "DynamoDB", "Storage", 25},
		{"dynamodb pitr", "DynamoDB", "PITR Backup", 25},
		{"cloudwatch logs ingestion", "CloudWatch Logs", "Ingestion", 10},
		{"cloudwatch logs storage", "CloudWatch Logs", "Storage", 10},
		{"eventbridge archive events", "EventBridge", "Archive Events", 1_000_000},
		{"eventbridge storage", "EventBridge", "Storage", 25},
		{"unknown service falls back to global", "SomeUnknownService", "", defaultUsageQty},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qty, isDefault := resolveUsageQty("any-resource", tt.service, tt.subLabel, "", nil)
			if qty != tt.wantQty {
				t.Errorf("qty = %v, want %v", qty, tt.wantQty)
			}
			if !isDefault {
				t.Errorf("expected isDefault=true for default path")
			}
		})
	}
}

func TestResolveUsageQty_UserSuppliedOverridesDefault(t *testing.T) {
	usageMap := map[string]float64{"my-table": 500}
	qty, isDefault := resolveUsageQty("my-table", "DynamoDB", "Storage", "", usageMap)
	if qty != 500 {
		t.Errorf("qty = %v, want 500", qty)
	}
	if isDefault {
		t.Errorf("expected isDefault=false for user-supplied value")
	}
}

func TestResolveUsageQty_SubLabelKeyOverridesBareKey(t *testing.T) {
	// --usage my-lambda/Requests=5000000 should take priority over --usage my-lambda=1000000
	usageMap := map[string]float64{
		"my-lambda":          1_000_000,
		"my-lambda/Requests": 5_000_000,
		"my-lambda/Duration": 800_000,
	}

	reqQty, reqDefault := resolveUsageQty("my-lambda", "Lambda", "Requests", "", usageMap)
	if reqQty != 5_000_000 {
		t.Errorf("Requests qty = %v, want 5000000", reqQty)
	}
	if reqDefault {
		t.Errorf("Requests: expected isDefault=false")
	}

	durQty, durDefault := resolveUsageQty("my-lambda", "Lambda", "Duration", "", usageMap)
	if durQty != 800_000 {
		t.Errorf("Duration qty = %v, want 800000", durQty)
	}
	if durDefault {
		t.Errorf("Duration: expected isDefault=false")
	}
}

func TestResolveUsageQty_BareKeyAppliesToAllSubLabels(t *testing.T) {
	// --usage my-lambda=2000000 applies to both Requests and Duration
	// when no name/SubLabel key is present
	usageMap := map[string]float64{"my-lambda": 2_000_000}

	reqQty, reqDefault := resolveUsageQty("my-lambda", "Lambda", "Requests", "", usageMap)
	if reqQty != 2_000_000 {
		t.Errorf("Requests qty = %v, want 2000000", reqQty)
	}
	if reqDefault {
		t.Errorf("Requests: expected isDefault=false")
	}

	durQty, durDefault := resolveUsageQty("my-lambda", "Lambda", "Duration", "", usageMap)
	if durQty != 2_000_000 {
		t.Errorf("Duration qty = %v, want 2000000", durQty)
	}
	if durDefault {
		t.Errorf("Duration: expected isDefault=false")
	}
}

func TestResolveUsageQty_RawTypeOverridesServiceDefault(t *testing.T) {
	tests := []struct {
		name    string
		rawType string
		wantQty float64
	}{
		{"metric alarm = 1", "aws:cloudwatch/metricAlarm:MetricAlarm", 1},
		{"composite alarm = 1", "aws:cloudwatch/compositeAlarm:CompositeAlarm", 1},
		{"dashboard = 730", "aws:cloudwatch/dashboard:Dashboard", 730},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qty, isDefault := resolveUsageQty("any-resource", "", "", tt.rawType, nil)
			if qty != tt.wantQty {
				t.Errorf("qty = %v, want %v", qty, tt.wantQty)
			}
			if !isDefault {
				t.Errorf("expected isDefault=true")
			}
		})
	}
}
