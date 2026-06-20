package api

import "encoding/json"

// AttrValue is a JSON value that can be string, number, array, bool, or null.
type AttrValue struct {
	raw json.RawMessage
}

func (a *AttrValue) UnmarshalJSON(data []byte) error {
	a.raw = data
	return nil
}

func (a AttrValue) MarshalJSON() ([]byte, error) {
	if a.raw == nil {
		return []byte("null"), nil
	}
	return a.raw, nil
}

func (a AttrValue) String() string {
	if a.raw == nil {
		return ""
	}
	// Try string
	var s string
	if err := json.Unmarshal(a.raw, &s); err == nil {
		return s
	}
	// Try number
	var n json.Number
	if err := json.Unmarshal(a.raw, &n); err == nil {
		return n.String()
	}
	// Try bool
	var b bool
	if err := json.Unmarshal(a.raw, &b); err == nil {
		if b {
			return "true"
		}
		return "false"
	}
	// Try array
	var arr []json.RawMessage
	if err := json.Unmarshal(a.raw, &arr); err == nil {
		parts := make([]string, 0, len(arr))
		for _, v := range arr {
			var sv string
			if err := json.Unmarshal(v, &sv); err == nil {
				parts = append(parts, sv)
			} else {
				parts = append(parts, string(v))
			}
		}
		result := ""
		for i, p := range parts {
			if i > 0 {
				result += ", "
			}
			result += p
		}
		return result
	}
	return string(a.raw)
}

// JSONValue returns the raw JSON as a string (for numeric/general use).
func (a AttrValue) JSONValue() string {
	if a.raw == nil {
		return "null"
	}
	return string(a.raw)
}

// PriceRate is a single tier within a price model.
type PriceRate struct {
	Price      *AttrValue `json:"price,omitempty"`
	StartRange *AttrValue `json:"startRange,omitempty"`
	EndRange   *AttrValue `json:"endRange,omitempty"`
}

// Price is a single pricing model entry.
type Price struct {
	Rates              []PriceRate `json:"rates,omitempty"`
	PricingModel       *string     `json:"pricingModel,omitempty"`
	UpfrontFee         *AttrValue  `json:"upfrontFee,omitempty"`
	Year               *AttrValue  `json:"year,omitempty"`
	Unit               *string     `json:"unit,omitempty"`
	PurchaseOption     *string     `json:"purchaseOption,omitempty"`
	InterruptionMaxPct *AttrValue  `json:"interruptionMaxPct,omitempty"`
}

// PricingItem is one result row from the pricing API.
type PricingItem struct {
	Product    string                `json:"product"`
	Provider   string                `json:"provider"`
	Region     string                `json:"region"`
	Attributes map[string]*AttrValue `json:"attributes"`
	Prices     []Price               `json:"prices"`
	MinPrice   *AttrValue            `json:"minPrice,omitempty"`
	MaxPrice   *AttrValue            `json:"maxPrice,omitempty"`
}

// PricingAPIResponse is the top-level response from POST /pricing.
type PricingAPIResponse struct {
	Data  []PricingItem `json:"data"`
	Total int64         `json:"total"`
}

// BatchPricingApiResponse is the top-level response from POST /pricing/batch.
// The API returns a JSON object keyed by product (e.g. {"ec2": [...]}).
type BatchPricingApiResponse map[string][]PricingItem

// PricingRequest is the POST body for /pricing.
type PricingRequest struct {
	Attrs  map[string]string `json:"attrs"`
	Prices []string          `json:"prices"`
}

type BatchPricingRequestItem struct {
	Provider string            `json:"provider"`
	Region   string            `json:"region"`
	Product  string            `json:"product,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Price    string            `json:"price,omitempty"`
}

type BatchPricingRequest struct {
	Requests []BatchPricingRequestItem `json:"requests"`
}

// MetadataResponse is the response from GET /pricing/metadata.
type MetadataResponse struct {
	ProductRegions  map[string][]string            `json:"product_regions"`
	ProductAttrs    map[string][]string            `json:"product_attrs"`
	AttributeValues map[string]map[string][]string `json:"attribute_values"`
	ProductGroups   map[string]uint64              `json:"product_groups"`
	PulumiResources map[string]PulumiResourceDef   `json:"pulumi_resources"`
	FreeTypes       []string                       `json:"free_types"`
}

// PulumiResourceDef describes how to decode a Pulumi resource type for pricing.
type PulumiResourceDef struct {
	Provider string                       `json:"provider"`
	Product  string                       `json:"product,omitempty"`
	Attrs    map[string]PulumiAttrMapping `json:"attrs"`
}

// PulumiAttrMapping maps a canonical pricing attribute to a Pulumi input field.
type PulumiAttrMapping struct {
	Input   string            `json:"input,omitempty"`   // Pulumi input property name (supports dot-path like "sku.name")
	Default string            `json:"default,omitempty"` // fallback value when input is missing
	Map     map[string]string `json:"map,omitempty"`     // optional value translation (e.g. "postgres" → "PostgreSQL")
}

// GenerateTokenResponse is the response from POST /api/auth/generate-token.
type GenerateTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExchangeCode string `json:"exchange_code"`
}

func (g *GenerateTokenResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		AccessToken  string `json:"access_token"`
		Token        string `json:"token"`
		ExchangeCode string `json:"exchange_code"`
		ExchangeID   string `json:"exchange_id"`
		ExchangeId2  string `json:"exchangeId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.AccessToken != "" {
		g.AccessToken = raw.AccessToken
	} else {
		g.AccessToken = raw.Token
	}
	if raw.ExchangeCode != "" {
		g.ExchangeCode = raw.ExchangeCode
	} else if raw.ExchangeID != "" {
		g.ExchangeCode = raw.ExchangeID
	} else {
		g.ExchangeCode = raw.ExchangeId2
	}
	return nil
}

// ExchangeResponse is the response from POST /api/auth/exchange.
type ExchangeResponse struct {
	Status *string `json:"status,omitempty"`
	CliID  *string `json:"cli_id,omitempty"`
	APIKey *string `json:"api_key,omitempty"`
}

func (e *ExchangeResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Status  string `json:"status"`
		CliID   string `json:"cli_id"`
		CliId2  string `json:"cliId"`
		APIKey  string `json:"api_key"`
		APIKey2 string `json:"apiKey"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Status != "" {
		e.Status = &raw.Status
	}
	cliID := raw.CliID
	if cliID == "" {
		cliID = raw.CliId2
	}
	if cliID != "" {
		e.CliID = &cliID
	}
	apiKey := raw.APIKey
	if apiKey == "" {
		apiKey = raw.APIKey2
	}
	if apiKey != "" {
		e.APIKey = &apiKey
	}
	return nil
}
