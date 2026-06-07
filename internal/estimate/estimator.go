package estimate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/api"
	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	"github.com/shopspring/decimal"
)

type batchPricingFetcher interface {
	FetchPricingBatch(api.BatchPricingRequest) (*api.BatchPricingApiResponse, error)
}

const defaultBatchPriceFilter = ">=0"

// hoursPerMonth is the standard monthly hours used for all hourly→monthly conversions.
const hoursPerMonth = 730

// defaultUsageQty is the monthly quantity assumed when the user does not
// provide a usage value for a usage-based resource (e.g. API Gateway requests).
const defaultUsageQty = 1_000_000

// serviceUsageDefaults maps "Service/SubLabel" (or just "Service") to a
// sensible per-service default monthly quantity. More specific keys
// (Service/SubLabel) take precedence over the bare Service key.
// All quantities are in the unit returned by the pricing API for that resource.
var serviceUsageDefaults = map[string]float64{
	// S3 — GB-Mo / requests
	"S3/Storage":      25,
	"S3/Requests-PUT": 10_000,
	"S3/Requests-GET": 100_000,

	// Lambda — requests / GB-seconds
	"Lambda/Requests": 1_000_000,
	"Lambda/Duration": 400_000, // GB-seconds

	// SNS — publish requests
	"SNS/Requests": 1_000_000,

	// API Gateway — requests
	"API Gateway/Requests": 1_000_000,

	// AppSync — invocations
	"AppSync/Invocations": 1_000_000,

	// DynamoDB — GB-Mo
	"DynamoDB/Storage":     25,
	"DynamoDB/PITR Backup": 25,

	// CloudWatch Logs — GB
	"CloudWatch Logs/Ingestion": 10,
	"CloudWatch Logs/Storage":   10,

	// CloudWatch Metric Streams — metric updates/mo
	"CloudWatch Metric Streams": 100_000,

	// CloudWatch Alarms — 1 Pulumi resource = 1 alarm = $0.10/mo
	"aws:cloudwatch/metricAlarm:MetricAlarm":       1,
	"aws:cloudwatch/compositeAlarm:CompositeAlarm": 1,

	// CloudWatch Dashboard — 1 resource = 730 dashboard-hours/mo (1 dashboard × 1 month)
	"aws:cloudwatch/dashboard:Dashboard": 730,

	// CloudWatch Contributor Insights — rules / events
	"CloudWatch Contributor Insights/Rule":           5,
	"CloudWatch Contributor Insights/Matched Events": 1_000_000,

	// CloudWatch Internet Monitor
	"CloudWatch Internet Monitor/Monitored Resources": 10,
	"CloudWatch Internet Monitor/City Networks":       1_000,

	// EventBridge
	"EventBridge/Archive Events": 1_000_000,
	"EventBridge/Storage":        25,

	// DynamoDB sub-resources
	"DynamoDB/Stream Reads":         1_000_000,
	"DynamoDB/Kinesis Data Capture": 1_000_000,
	"DynamoDB/Full Export":          100, // GB
	"DynamoDB/Incremental Export":   10,  // GB
}

// hourlyUnits are unit strings that indicate time-based (per-hour) pricing.
// Any unit NOT in this set is treated as usage-based.
var hourlyUnits = map[string]bool{
	"hrs":   true,
	"hours": true,
	"hour":  true,
	"hr":    true,
	// DynamoDB provisioned capacity — treated as hourly so HourlyQty scales the rate by RCU/WCU count.
	"readcapacityunit-hrs":  true,
	"writecapacityunit-hrs": true,
}

// isHourlyUnit returns true when the unit string represents time-based pricing.
func isHourlyUnit(unit string) bool {
	return hourlyUnits[strings.ToLower(strings.TrimSpace(unit))]
}

// ModelSelector specifies which pricing model to use for a resource.
// All fields are case-insensitive. Empty fields are treated as wildcards.
type ModelSelector struct {
	Model          string // e.g. "Reserved", "ComputeSavingsPlans", "spot"
	PurchaseOption string // e.g. "standard", "No Upfront" — optional
	Term           string // e.g. "1yr", "3yr" — optional
}

// termToHours converts a term string to the number of hours in that term.
// Returns 0 for unknown terms.
func termToHours(term string) int64 {
	switch strings.TrimSpace(strings.ToLower(term)) {
	case "1yr", "1y":
		return 8760
	case "3yr", "3y":
		return 26280
	default:
		return 0
	}
}

// amortizedHourlyRate returns the effective hourly rate for a PriceEntry,
// spreading any upfront fee evenly over the term hours.
// For entries with no upfront fee, this equals RatePerHr.
func amortizedHourlyRate(e resources.PriceEntry) (rate decimal.Decimal, isAmortized bool) {
	rate = e.RatePerHr
	if e.UpfrontFee == "" || e.UpfrontFee == "0" {
		return rate, false
	}
	upfront, err := decimal.NewFromString(e.UpfrontFee)
	if err != nil || upfront.IsZero() {
		return rate, false
	}
	termHours := termToHours(e.Term)
	if termHours <= 0 {
		return rate, false
	}
	amortized := upfront.Div(decimal.NewFromInt(termHours))
	return rate.Add(amortized), true
}

// applyModelSelector finds the best matching PriceEntry for the given selector,
// marks it as IsCurrent (and clears all others), and returns the effective
// hourly rate (with upfront amortized). Returns the original OnDemand rate
// and isAmortized=false when no match is found.
func applyModelSelector(entries []resources.PriceEntry, sel ModelSelector) (selected []resources.PriceEntry, effectiveRate decimal.Decimal, isAmortized bool) {
	// Find all candidates that match the selector.
	type candidate struct {
		idx  int
		rate decimal.Decimal
		amrt bool
	}
	var candidates []candidate

	for i, e := range entries {
		if !strings.EqualFold(strings.TrimSpace(e.Model), strings.TrimSpace(sel.Model)) {
			continue
		}
		if sel.PurchaseOption != "" && !strings.EqualFold(strings.TrimSpace(e.PurchaseOption), strings.TrimSpace(sel.PurchaseOption)) {
			continue
		}
		if sel.Term != "" && !strings.EqualFold(strings.TrimSpace(e.Term), strings.TrimSpace(sel.Term)) {
			continue
		}
		r, amrt := amortizedHourlyRate(e)
		candidates = append(candidates, candidate{i, r, amrt})
	}

	if len(candidates) == 0 {
		// No match — keep original IsCurrent flags, return OnDemand rate.
		for _, e := range entries {
			if e.IsCurrent {
				r, amrt := amortizedHourlyRate(e)
				return entries, r, amrt
			}
		}
		return entries, decimal.Zero, false
	}

	// Pick the candidate with the lowest effective rate.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.rate.LessThan(best.rate) {
			best = c
		}
	}

	// Update IsCurrent flags.
	result := make([]resources.PriceEntry, len(entries))
	copy(result, entries)
	for i := range result {
		result[i].IsCurrent = (i == best.idx)
	}

	return result, best.rate, best.amrt
}

// EstimateAllResources estimates costs for all resources.
// usageMap maps resource name → monthly quantity for usage-based resources.
// modelMap maps resource name (or "" for global) → ModelSelector to override the default OnDemand model.
// Pass nil or an empty map to use the built-in defaults.
func EstimateAllResources(client batchPricingFetcher, records []resources.DecodedResource, usageMap map[string]float64, modelMap map[string]ModelSelector) ([]resources.EstimateResult, error) {
	if client == nil {
		return nil, fmt.Errorf("nil pricing client")
	}
	if len(records) == 0 {
		return []resources.EstimateResult{}, nil
	}

	// Separate billable resources from no-pricing ones.
	var billable []resources.DecodedResource
	result := make([]resources.EstimateResult, 0, len(records))

	for _, record := range records {
		if record.NoPricing {
			statusMsg := "Sorry, not supported yet"
			if record.IsFreeType {
				statusMsg = "free resource"
			}
			result = append(result, resources.EstimateResult{
				ResourceName:   record.Name,
				SubLabel:       record.SubLabel,
				RawType:        record.RawType,
				Product:        record.RawType,
				Region:         record.Region,
				Props:          record.Props,
				InputsJSON:     record.InputsJSON,
				StatusMsg:      statusMsg,
				RegionFallback: record.RegionFallback,
			})
		} else {
			billable = append(billable, record)
		}
	}

	if len(billable) == 0 {
		return result, nil
	}

	requests := api.BatchPricingRequest{
		Requests: make([]api.BatchPricingRequestItem, 0, len(billable)),
	}

	for _, record := range billable {
		item := api.BatchPricingRequestItem{
			Provider: record.Provider,
			Region:   record.Region,
			Product:  record.Service,
			Attrs:    compactAttrs(record.Attrs),
			Price:    effectivePriceFilter(record.PriceFilter),
		}
		requests.Requests = append(requests.Requests, item)
	}

	prices, err := client.FetchPricingBatch(requests)
	if err != nil {
		return nil, err
	}

	for _, record := range billable {
		if prices == nil {
			result = append(result, resources.EstimateResult{
				ResourceName:   record.Name,
				SubLabel:       record.SubLabel,
				RawType:        record.RawType,
				Product:        displayProduct(record.Provider, record.Service),
				Region:         record.Region,
				Props:          record.Props,
				InputsJSON:     record.InputsJSON,
				StatusMsg:      "Sorry, not supported yet",
				RegionFallback: record.RegionFallback,
			})
			continue
		}

		item, ok := findMatchingPrice(record, *prices)
		if !ok {
			result = append(result, resources.EstimateResult{
				ResourceName:   record.Name,
				SubLabel:       record.SubLabel,
				RawType:        record.RawType,
				Product:        displayProduct(record.Provider, record.Service),
				Region:         record.Region,
				Props:          record.Props,
				InputsJSON:     record.InputsJSON,
				StatusMsg:      "Sorry, not supported yet",
				RegionFallback: record.RegionFallback,
			})
			continue
		}

		entries, onDemand := buildPriceEntries(item)

		// If every returned price is $0, treat the resource as free rather
		// than showing a confusing $0.00 table.
		if allZeroRates(entries) {
			result = append(result, resources.EstimateResult{
				ResourceName:   record.Name,
				SubLabel:       record.SubLabel,
				RawType:        record.RawType,
				Product:        displayProduct(item.Provider, item.Product),
				Region:         record.Region,
				Props:          record.Props,
				InputsJSON:     record.InputsJSON,
				StatusMsg:      "free resource",
				RegionFallback: record.RegionFallback,
			})
			continue
		}

		// Apply model selector: resource-specific key wins over global ("").
		effectiveRate := onDemand
		isAmortized := false
		if modelMap != nil {
			// Look up model selector: name/SubLabel first (most specific), then bare name, then global "".
			sel, ok := modelMap[record.Name+"/"+record.SubLabel]
			if !ok && record.SubLabel != "" {
				// bare name applies to all sub-labels
				sel, ok = modelMap[record.Name]
			}
			if !ok {
				sel, ok = modelMap[""]
			}
			if ok {
				entries, effectiveRate, isAmortized = applyModelSelector(entries, sel)
			} else {
				// No selector — amortize the OnDemand entry anyway (usually no upfront, so no-op).
				for _, e := range entries {
					if e.IsCurrent {
						effectiveRate, isAmortized = amortizedHourlyRate(e)
						break
					}
				}
			}
		} else {
			for _, e := range entries {
				if e.IsCurrent {
					effectiveRate, isAmortized = amortizedHourlyRate(e)
					break
				}
			}
		}
		isUsage, usageUnit := detectUsageBased(entries)

		// Apply per-hour quantity multiplier when set
		if record.HourlyQty.IsPositive() {
			effectiveRate = effectiveRate.Mul(record.HourlyQty)
			onDemand = onDemand.Mul(record.HourlyQty)
		}

		est := resources.EstimateResult{
			ResourceName:   record.Name,
			SubLabel:       record.SubLabel,
			RawType:        record.RawType,
			Product:        displayProduct(item.Provider, item.Product),
			Region:         record.Region,
			Props:          record.Props,
			InputsJSON:     record.InputsJSON,
			Prices:         entries,
			OnDemandRate:   onDemand,
			EffectiveRate:  effectiveRate,
			IsAmortized:    isAmortized,
			IsUsageBased:   isUsage,
			UsageUnit:      usageUnit,
			RegionFallback: record.RegionFallback,
		}

		if isUsage {
			qty, isDefault := resolveUsageQty(record.Name, record.Service, record.SubLabel, record.RawType, usageMap)
			est.UsageQty = qty
			est.UsageDefault = isDefault
			est.UsageMonthly = calcUsageMonthlyCost(entries, qty)
			// Clear OnDemandRate and EffectiveRate for usage-based resources
			// so the hourly totals box doesn't include them.
			est.OnDemandRate = decimal.Zero
			est.EffectiveRate = decimal.Zero
		}

		result = append(result, est)
	}

	return result, nil
}

// buildPriceEntries converts an api.PricingItem into a sorted slice of PriceEntry.
// OnDemand is always first; the rest are sorted by model name, then rate.
func buildPriceEntries(item api.PricingItem) ([]resources.PriceEntry, decimal.Decimal) {
	var entries []resources.PriceEntry
	var onDemandRate decimal.Decimal

	for _, p := range item.Prices {
		model := ""
		if p.PricingModel != nil {
			model = *p.PricingModel
		}
		purchaseOption := ""
		if p.PurchaseOption != nil {
			purchaseOption = *p.PurchaseOption
		}
		term := ""
		if p.Year != nil {
			term = p.Year.String()
		}
		upfront := ""
		if p.UpfrontFee != nil {
			upfront = p.UpfrontFee.String()
		}
		unit := ""
		if p.Unit != nil {
			unit = *p.Unit
		}

		rate := decimal.Zero
		if len(p.Rates) > 0 && p.Rates[0].Price != nil {
			if d, err := decimal.NewFromString(p.Rates[0].Price.String()); err == nil {
				rate = d
			}
		}

		isCurrent := strings.EqualFold(model, "OnDemand")
		if isCurrent && rate.GreaterThan(onDemandRate) {
			onDemandRate = rate
		}

		isUsage := !isHourlyUnit(unit)

		// Build rate tiers for volume-based pricing (more than one rate).
		var tiers []resources.RateTier
		if len(p.Rates) > 1 {
			for _, r := range p.Rates {
				tier := resources.RateTier{}
				if r.Price != nil {
					tier.Price = r.Price.String()
				}
				if r.StartRange != nil {
					tier.StartRange = r.StartRange.String()
				}
				if r.EndRange != nil {
					tier.EndRange = r.EndRange.String()
				}
				tiers = append(tiers, tier)
			}
		}

		entries = append(entries, resources.PriceEntry{
			Model:          model,
			PurchaseOption: purchaseOption,
			Term:           term,
			UpfrontFee:     upfront,
			RatePerHr:      rate,
			Unit:           unit,
			IsCurrent:      isCurrent,
			IsUsageBased:   isUsage,
			Tiers:          tiers,
		})
	}

	// Sort: OnDemand first, then by model name, then by rate ascending.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsCurrent != entries[j].IsCurrent {
			return entries[i].IsCurrent
		}
		if entries[i].Model != entries[j].Model {
			return entries[i].Model < entries[j].Model
		}
		return entries[i].RatePerHr.LessThan(entries[j].RatePerHr)
	})

	return entries, onDemandRate
}

// allZeroRates returns true when every entry has a zero rate across all tiers,
// meaning the resource is effectively free. For tiered pricing, any non-zero
// tier price means the resource is not free (the zero tier is just a free allowance).
func allZeroRates(entries []resources.PriceEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, e := range entries {
		if !e.RatePerHr.IsZero() {
			return false
		}
		// Check tiered pricing — a non-zero price in any tier means not free.
		for _, t := range e.Tiers {
			if d, err := decimal.NewFromString(t.Price); err == nil && !d.IsZero() {
				return false
			}
		}
	}
	return true
}

func effectivePriceFilter(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultBatchPriceFilter
	}
	return trimmed
}

func findMatchingPrice(record resources.DecodedResource, response map[string][]api.PricingItem) (api.PricingItem, bool) {
	for _, items := range response {
		for _, item := range items {
			if pricingItemMatchesRecord(item, record) {
				return item, true
			}
		}
	}
	return api.PricingItem{}, false
}

func pricingItemMatchesRecord(item api.PricingItem, record resources.DecodedResource) bool {
	if record.Provider != "" && !equalFoldTrim(item.Provider, record.Provider) {
		return false
	}
	if record.Region != "" && !equalFoldTrim(item.Region, record.Region) {
		return false
	}
	// Only enforce product match when both sides are non-empty.
	// Some APIs return product="" for certain services (e.g. DynamoDB); in
	// that case we fall through to attr-level matching.
	if record.Service != "" && item.Product != "" && !equalFoldTrim(item.Product, record.Service) {
		return false
	}

	expectedAttrs := compactAttrs(record.Attrs)
	for key, expectedValue := range expectedAttrs {
		actualValue, ok := lookupAttrCaseInsensitive(item.Attributes, key)
		if !ok || actualValue == nil || !equalFoldTrim(actualValue.String(), expectedValue) {
			return false
		}
	}

	return true
}

func lookupAttrCaseInsensitive(attrs map[string]*api.AttrValue, key string) (*api.AttrValue, bool) {
	if value, ok := attrs[key]; ok {
		return value, true
	}

	for actualKey, value := range attrs {
		if equalFoldTrim(actualKey, key) {
			return value, true
		}
	}

	return nil, false
}

func equalFoldTrim(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func compactAttrs(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}

	compacted := make(map[string]string)
	for key, value := range attrs {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		compacted[key] = trimmed
	}

	if len(compacted) == 0 {
		return nil
	}

	return compacted
}

func displayProduct(provider, product string) string {
	switch {
	case provider != "" && product != "":
		return provider + " " + product
	case product != "":
		return product
	default:
		return provider
	}
}

// detectUsageBased returns (true, unit) when the dominant pricing unit is not
// time-based (i.e. not "Hrs"/"Hours"). It picks the unit from the first
// OnDemand entry, falling back to the first entry overall.
func detectUsageBased(entries []resources.PriceEntry) (bool, string) {
	for _, e := range entries {
		if e.IsCurrent {
			return !isHourlyUnit(e.Unit), e.Unit
		}
	}
	if len(entries) > 0 {
		return !isHourlyUnit(entries[0].Unit), entries[0].Unit
	}
	return false, ""
}

// resolveUsageQty returns the monthly quantity to use for a resource.
// Priority: usageMap (user-supplied) > rawType key > service/subLabel key > service key > global default.
// All keys are looked up in serviceUsageDefaults — rawType (e.g. "aws:cloudwatch/metricAlarm:MetricAlarm"),
// "Service/SubLabel", and bare "Service" are all valid keys in that map.
func resolveUsageQty(resourceName, service, subLabel, rawType string, usageMap map[string]float64) (qty float64, isDefault bool) {
	// 1. User-supplied value takes highest priority.
	if usageMap != nil {
		// 1a. Exact name/SubLabel match — most specific, for 1:N resources
		//     e.g. --usage my-lambda/Requests=5000000
		if subLabel != "" {
			if v, ok := usageMap[resourceName+"/"+subLabel]; ok && v > 0 {
				return v, false
			}
		}
		// 1b. Bare name match — applies to all sub-labels of this resource
		//     e.g. --usage my-lambda=5000000
		if v, ok := usageMap[resourceName]; ok && v > 0 {
			return v, false
		}
	}
	// 2. rawType exact match (e.g. "aws:cloudwatch/metricAlarm:MetricAlarm" → 1).
	if rawType != "" {
		if v, ok := serviceUsageDefaults[rawType]; ok {
			return v, true
		}
	}
	// 3. Per-service/subLabel default — try exact then case-insensitive.
	if subLabel != "" {
		key := service + "/" + subLabel
		if v, ok := serviceUsageDefaults[key]; ok {
			return v, true
		}
		keyLower := strings.ToLower(key)
		for k, v := range serviceUsageDefaults {
			if strings.ToLower(k) == keyLower {
				return v, true
			}
		}
	}
	// 4. Per-service default — try exact then case-insensitive.
	if service != "" {
		if v, ok := serviceUsageDefaults[service]; ok {
			return v, true
		}
		serviceLower := strings.ToLower(service)
		for k, v := range serviceUsageDefaults {
			if strings.ToLower(k) == serviceLower {
				return v, true
			}
		}
	}
	// 5. Global fallback.
	return defaultUsageQty, true
}

// calcUsageMonthlyCost computes the monthly cost for a usage-based resource
// given a monthly quantity. It uses the OnDemand entry's tiers when available,
// otherwise falls back to the flat rate.
func calcUsageMonthlyCost(entries []resources.PriceEntry, monthlyQty float64) decimal.Decimal {
	// Find the OnDemand entry.
	var target *resources.PriceEntry
	for i := range entries {
		if entries[i].IsCurrent {
			target = &entries[i]
			break
		}
	}
	if target == nil && len(entries) > 0 {
		target = &entries[0]
	}
	if target == nil {
		return decimal.Zero
	}

	qty := decimal.NewFromFloat(monthlyQty)

	if len(target.Tiers) == 0 {
		// Flat rate: price per unit × quantity.
		return target.RatePerHr.Mul(qty)
	}

	// Tiered pricing: walk through tiers and accumulate cost.
	return calcTieredCost(target.Tiers, qty)
}

// calcTieredCost applies volume-tiered pricing to a total quantity.
// Each tier covers [startRange, endRange) units at its price.
func calcTieredCost(tiers []resources.RateTier, totalQty decimal.Decimal) decimal.Decimal {
	remaining := totalQty
	total := decimal.Zero

	for _, tier := range tiers {
		if remaining.IsZero() || remaining.IsNegative() {
			break
		}

		price, err := decimal.NewFromString(tier.Price)
		if err != nil {
			continue
		}

		start := decimal.Zero
		if tier.StartRange != "" {
			if d, err := decimal.NewFromString(tier.StartRange); err == nil {
				start = d
			}
		}

		// endRange of "" or "Inf" means unlimited.
		isInf := tier.EndRange == "" ||
			strings.EqualFold(tier.EndRange, "inf") ||
			strings.EqualFold(tier.EndRange, "infinity")

		var tierSize decimal.Decimal
		if isInf {
			tierSize = remaining
		} else {
			if end, err := decimal.NewFromString(tier.EndRange); err == nil {
				tierSize = end.Sub(start)
			} else {
				tierSize = remaining
			}
		}

		units := remaining
		if units.GreaterThan(tierSize) {
			units = tierSize
		}

		total = total.Add(price.Mul(units))
		remaining = remaining.Sub(units)
	}

	return total
}

// formatUsageQty formats a usage quantity for display (e.g. 1000000 → "1,000,000").
func formatUsageQty(qty float64) string {
	s := strconv.FormatFloat(qty, 'f', 0, 64)
	// Insert thousand separators.
	n := len(s)
	if n <= 3 {
		return s
	}
	var out []byte
	for i, c := range s {
		if i > 0 && (n-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}
