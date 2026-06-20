package resources

import (
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/shopspring/decimal"
)

type ResourceRecord struct {
	Type             string
	Name             string
	ID               string
	Inputs           resource.PropertyMap
	MockedProperties map[string]string
}

type DecodedResource struct {
	Provider       string
	Region         string
	Service        string
	Name           string
	SubLabel       string            // non-empty when one resource produces multiple pricing queries (e.g. "Requests", "Duration")
	RawType        string            // original Pulumi resource type, e.g. "aws:ec2/securityGroup:SecurityGroup"
	Attrs          map[string]string // pricing attributes used for client-side match filtering
	QueryAttrs     map[string]string // when non-nil, sent to the API instead of Attrs (use when query filters differ from match filters)
	Props          map[string]string // display properties (instance type, region, etc.)
	InputsJSON     string            // formatted Pulumi input properties for debugging/inspection
	PriceFilter    string
	NoPricing      bool            // true for resources that don't have pricing (e.g. security groups)
	IsFreeType     bool            // true when the resource type is in the metadata free_types list
	RegionFallback bool            // true when region was not detected and us-east-1 was used as default
	HourlyQty      decimal.Decimal // quantity multiplier applied to the hourly rate (e.g. vCPU count × task count)
	HourlyQtyLabel string          // optional display label for HourlyQty (e.g. "256 × 3 tasks"); overrides numeric display
}

// PriceEntry is one pricing option for a resource.
type PriceEntry struct {
	Model          string          // OnDemand, Reserved, spot, ComputeSavingsPlans, EC2InstanceSavingsPlans
	PurchaseOption string          // standard, convertible, All Upfront, Partial Upfront, No Upfront
	Term           string          // 1yr, 3yr, or empty for on-demand/spot
	UpfrontFee     string          // dollar amount or empty
	RatePerHr      decimal.Decimal // hourly rate (first tier for tiered pricing)
	Unit           string          // Hrs, Requests, etc.
	IsCurrent      bool            // true for the OnDemand price (what you pay right now)
	IsUsageBased   bool            // true when unit is not time-based (e.g. Requests, Messages, GB)
	Tiers          []RateTier      // non-nil when pricing is volume-tiered
}

// RateTier is a single tier within volume-based pricing.
type RateTier struct {
	Price      string // e.g. "0.0000035000"
	StartRange string // e.g. "0"
	EndRange   string // e.g. "333000000", "Inf"
}

type EstimateResult struct {
	ResourceName string
	SubLabel     string // non-empty when grouped under a parent resource (e.g. "Requests", "Duration")
	RawType      string
	Product      string
	Region       string
	Props        map[string]string
	InputsJSON   string          // formatted Pulumi input properties, empty for non-Pulumi estimates
	Prices       []PriceEntry    // structured pricing options; nil if no pricing
	OnDemandRate decimal.Decimal // convenience: the OnDemand hourly rate (zero if unknown)
	// EffectiveRate is the rate used for totals — equals OnDemandRate for OnDemand,
	// or amortized(RatePerHr + Upfront/termHours) when a non-OnDemand model is selected.
	EffectiveRate decimal.Decimal
	// IsAmortized is true when EffectiveRate includes an amortized upfront fee.
	IsAmortized bool
	StatusMsg   string // non-empty when pricing lookup failed
	// Usage-based fields
	IsUsageBased bool            // true when pricing is per-request/message/GB rather than per-hour
	UsageUnit    string          // e.g. "Requests", "Messages", "GB"
	UsageQty     float64         // monthly quantity used for estimation (user-supplied or default)
	UsageDefault bool            // true when UsageQty came from the built-in default
	UsageMonthly decimal.Decimal // estimated monthly cost = f(tiers, UsageQty)
	// HourlyQty is the per-resource quantity multiplier applied to the base hourly rate
	// (e.g. vCPU count × task count for Fargate, RCU/WCU for DynamoDB provisioned).
	// Zero when no multiplier is set.
	HourlyQty decimal.Decimal
	// HourlyQtyLabel is a human-readable label for HourlyQty (e.g. "256 × 3 tasks").
	// When non-empty, used in the totals table instead of the raw HourlyQty number.
	HourlyQtyLabel string
	// BaseRate is the per-unit hourly rate before HourlyQty is applied.
	// Zero when HourlyQty is not set.
	BaseRate decimal.Decimal
	// Region metadata
	RegionFallback bool // true when region was not detected and us-east-1 was used
}
