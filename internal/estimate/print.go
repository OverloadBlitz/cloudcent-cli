package estimate

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/shopspring/decimal"
)

// JSONTier is a single volume-pricing tier for JSON output.
type JSONTier struct {
	StartRange string `json:"start_range"`
	EndRange   string `json:"end_range"`
	Price      string `json:"price"`
}

// JSONPriceEntry is one pricing option for JSON output.
type JSONPriceEntry struct {
	Model          string     `json:"model"`
	PurchaseOption string     `json:"purchase_option,omitempty"`
	Term           string     `json:"term,omitempty"`
	UpfrontFee     string     `json:"upfront_fee,omitempty"`
	RatePerHr      string     `json:"rate_per_hr"`
	Unit           string     `json:"unit"`
	IsCurrent      bool       `json:"is_current"`
	Tiers          []JSONTier `json:"tiers,omitempty"`
}

// JSONResource is one resource entry for JSON output.
type JSONResource struct {
	Name           string            `json:"name"`
	SubLabel       string            `json:"sub_label,omitempty"`
	Product        string            `json:"product"`
	Region         string            `json:"region,omitempty"`
	RegionFallback bool              `json:"region_fallback,omitempty"`
	Props          map[string]string `json:"props,omitempty"`
	Prices         []JSONPriceEntry  `json:"prices,omitempty"`
	OnDemandRate   string            `json:"on_demand_rate,omitempty"`
	Status         string            `json:"status,omitempty"`
	// Usage-based fields
	IsUsageBased bool    `json:"is_usage_based,omitempty"`
	UnitRate     string  `json:"unit_rate,omitempty"`
	UsageUnit    string  `json:"usage_unit,omitempty"`
	UsageQty     float64 `json:"usage_qty,omitempty"`
	UsageDefault bool    `json:"usage_default,omitempty"`
	UsageMonthly string  `json:"cost_monthly,omitempty"`
}

// JSONTotals holds the cost summary for JSON output.
type JSONTotals struct {
	HourlyRate    string `json:"hourly_rate"`
	MonthlyHourly string `json:"monthly_hourly"`
	MonthlyUsage  string `json:"monthly_usage,omitempty"`
	MonthlyTotal  string `json:"monthly_total"`
}

// JSONOutput is the top-level JSON structure.
type JSONOutput struct {
	Resources []JSONResource `json:"resources"`
	Totals    JSONTotals     `json:"totals"`
}

// PrintResultsJSON writes the estimate results as a JSON document to stdout.
// It mirrors the same data as PrintResults but in a machine-readable format.
func PrintResultsJSON(results []resources.EstimateResult) {
	totalHourly := decimal.Zero
	totalUsageMonthly := decimal.Zero

	jsonResources := make([]JSONResource, 0, len(results))
	for _, r := range results {
		jr := JSONResource{
			Name:           r.ResourceName,
			SubLabel:       r.SubLabel,
			Product:        r.Product,
			RegionFallback: r.RegionFallback,
			Props:          r.Props,
			Status:         r.StatusMsg,
		}

		// Extract region from Props if present.
		if r.Props != nil {
			if region, ok := r.Props["region"]; ok {
				jr.Region = region
			}
		}

		if r.OnDemandRate.IsPositive() {
			jr.OnDemandRate = r.OnDemandRate.String()
			totalHourly = totalHourly.Add(r.EffectiveRate)
		}

		if r.IsUsageBased {
			jr.IsUsageBased = true
			jr.UsageUnit = r.UsageUnit
			jr.UsageQty = r.UsageQty
			jr.UsageDefault = r.UsageDefault
			jr.UsageMonthly = r.UsageMonthly.String()
			if r.UsageMonthly.IsPositive() {
				totalUsageMonthly = totalUsageMonthly.Add(r.UsageMonthly)
			}
		}

		for _, p := range r.Prices {
			jp := JSONPriceEntry{
				Model:          p.Model,
				PurchaseOption: p.PurchaseOption,
				Term:           p.Term,
				UpfrontFee:     p.UpfrontFee,
				RatePerHr:      p.RatePerHr.String(),
				Unit:           p.Unit,
				IsCurrent:      p.IsCurrent,
			}
			for _, t := range p.Tiers {
				jp.Tiers = append(jp.Tiers, JSONTier{
					StartRange: t.StartRange,
					EndRange:   t.EndRange,
					Price:      t.Price,
				})
			}
			jr.Prices = append(jr.Prices, jp)
		}

		if jr.IsUsageBased {
			for _, jp := range jr.Prices {
				if jp.IsCurrent {
					if len(jp.Tiers) > 0 {
						jr.UnitRate = jp.Tiers[0].Price
					} else {
						jr.UnitRate = jp.RatePerHr
					}
					break
				}
			}
		}

		jsonResources = append(jsonResources, jr)
	}

	monthly := totalHourly.Mul(decimal.NewFromInt(hoursPerMonth))
	monthlyTotal := monthly.Add(totalUsageMonthly)

	out := JSONOutput{
		Resources: jsonResources,
		Totals: JSONTotals{
			HourlyRate:    totalHourly.String(),
			MonthlyHourly: monthly.String(),
			MonthlyUsage:  totalUsageMonthly.String(),
			MonthlyTotal:  monthlyTotal.String(),
		},
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

var (
	colHeader  = lipgloss.Color("#94A3B8")
	colBorder  = lipgloss.Color("#475569")
	colCurrent = lipgloss.Color("#22C55E")
	colMuted   = lipgloss.Color("#64748B")
	colTitle   = lipgloss.Color("#FFFFFF")
	colWarn    = lipgloss.Color("#F59E0B")
	colFree    = lipgloss.Color("#22C55E")
)

// resultGroup holds one or more EstimateResults that share the same resource name.
// When a resource produces multiple pricing queries (e.g. Lambda → Requests + Duration),
// they are rendered under a single resource header.
type resultGroup struct {
	results []resources.EstimateResult
}

// groupResults groups consecutive results that share the same ResourceName
// and have a non-empty SubLabel. Ungrouped results (SubLabel == "") get their
// own single-element group.
func groupResults(results []resources.EstimateResult) []resultGroup {
	var groups []resultGroup
	i := 0
	for i < len(results) {
		r := results[i]
		if r.SubLabel == "" {
			groups = append(groups, resultGroup{results: []resources.EstimateResult{r}})
			i++
			continue
		}
		// Collect consecutive results with the same ResourceName.
		j := i + 1
		for j < len(results) && results[j].ResourceName == r.ResourceName && results[j].SubLabel != "" {
			j++
		}
		groups = append(groups, resultGroup{results: results[i:j]})
		i = j
	}
	return groups
}

// PrintResults renders per-resource pricing tables and a final cost summary.
// Shared by `cloudcent pulumi estimate` and `cloudcent diagram estimate`.
func PrintResults(results []resources.EstimateResult) {
	titleSt := lipgloss.NewStyle().Foreground(colTitle).Bold(true)
	mutedSt := lipgloss.NewStyle().Foreground(colMuted)
	warnSt := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	subLabelSt := lipgloss.NewStyle().Foreground(colHeader).Bold(true)

	var regionFallbackNames []string
	regionFallbackProviders := map[string]bool{}

	groups := groupResults(results)

	for i, g := range groups {
		first := g.results[0]
		fmt.Println()
		fmt.Printf("%s  %s\n",
			titleSt.Render(fmt.Sprintf("[%d] %s", i+1, first.ResourceName)),
			mutedSt.Render("("+first.Product+")"),
		)

		if len(first.Props) > 0 {
			propKeys := make([]string, 0, len(first.Props))
			for k := range first.Props {
				propKeys = append(propKeys, k)
			}
			sort.Strings(propKeys)
			for _, k := range propKeys {
				fmt.Printf("    %s %s\n",
					mutedSt.Render(fmt.Sprintf("%-18s", k)),
					first.Props[k],
				)
			}
		}

		if first.InputsJSON != "" {
			fmt.Printf("    %s\n", mutedSt.Render("Input properties:"))
			fmt.Println(indent(first.InputsJSON, "      "))
		}

		if first.RegionFallback {
			regionFallbackNames = append(regionFallbackNames, first.ResourceName)
			// Infer provider from resource type string.
			rawType := first.Props["type"]
			switch {
			case strings.HasPrefix(rawType, "azure-native:") || strings.HasPrefix(rawType, "azure:"):
				regionFallbackProviders["azure"] = true
			case strings.HasPrefix(rawType, "gcp:"):
				regionFallbackProviders["gcp"] = true
			case strings.HasPrefix(rawType, "oci:"):
				regionFallbackProviders["oci"] = true
			default:
				regionFallbackProviders["aws"] = true
			}
			fmt.Printf("    %s\n", warnSt.Render(fmt.Sprintf("⚠ Region not detected — using %q as fallback", first.Region)))
		}

		for _, r := range g.results {
			if r.SubLabel != "" {
				fmt.Printf("\n    %s\n", subLabelSt.Render("── "+r.SubLabel+" ──"))
			}

			if r.StatusMsg != "" {
				var msgSt lipgloss.Style
				if strings.Contains(r.StatusMsg, "free") {
					msgSt = lipgloss.NewStyle().Foreground(colFree)
				} else {
					msgSt = lipgloss.NewStyle().Foreground(colWarn)
				}
				fmt.Printf("    %s %s\n", mutedSt.Render("Pricing:"), msgSt.Render(r.StatusMsg))
				continue
			}

			if len(r.Prices) == 0 {
				fmt.Printf("    %s no data\n", mutedSt.Render("Pricing:"))
				continue
			}

			fmt.Println(renderPricesTableWithUsage(r.Prices, r.UsageQty))

			// Show effective rate when upfront has been amortized.
			if r.IsAmortized && r.EffectiveRate.IsPositive() {
				amortSt := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
				fmt.Printf("    %s %s\n",
					mutedSt.Render(fmt.Sprintf("%-18s", "Effective rate")),
					amortSt.Render("$"+r.EffectiveRate.StringFixed(10)+"/hr (upfront amortized)"),
				)
			}

			if r.IsUsageBased {
				if r.UsageDefault {
					defaultQtySt := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
					usageKey := r.ResourceName
					if r.SubLabel != "" {
						usageKey = r.ResourceName + "/" + r.SubLabel
					}
					qtyPart := defaultQtySt.Render(formatUsageQty(r.UsageQty)+" "+r.UsageUnit+"/mo") +
						" " + mutedSt.Render(fmt.Sprintf("(default — use --usage %s=<qty> to override)", usageKey))
					fmt.Printf("    %s %s  →  %s\n",
						mutedSt.Render(fmt.Sprintf("%-18s", "Usage estimate")),
						qtyPart,
						titleSt.Render("$"+r.UsageMonthly.StringFixed(10)+" / mo"),
					)
				} else {
					qtyLabel := formatUsageQty(r.UsageQty) + " " + r.UsageUnit + "/mo"
					fmt.Printf("    %s %s  →  %s\n",
						mutedSt.Render(fmt.Sprintf("%-18s", "Usage estimate")),
						mutedSt.Render(qtyLabel),
						titleSt.Render("$"+r.UsageMonthly.StringFixed(10)+" / mo"),
					)
				}
			}
		}
	}

	// Totals
	totalHourly := decimal.Zero
	totalUsageMonthly := decimal.Zero
	hasHourlyCost := false
	hasUsageCost := false

	for _, r := range results {
		if r.EffectiveRate.IsPositive() {
			totalHourly = totalHourly.Add(r.EffectiveRate)
			hasHourlyCost = true
		}
		if r.IsUsageBased && r.UsageMonthly.IsPositive() {
			totalUsageMonthly = totalUsageMonthly.Add(r.UsageMonthly)
			hasUsageCost = true
		}
	}

	fmt.Println()
	if hasHourlyCost || hasUsageCost {
		monthly := totalHourly.Mul(decimal.NewFromInt(hoursPerMonth))
		monthlyTotal := monthly.Add(totalUsageMonthly)
		fmt.Println(renderTotalsTable(results, monthlyTotal))
	} else {
		fmt.Println(mutedSt.Render("Total: no billable resources found"))
	}

	// Region fallback notice
	if len(regionFallbackNames) > 0 {
		fmt.Println()
		fmt.Println(warnSt.Render(" Region fallback notice"))
		fmt.Println(mutedSt.Render("  The following resources had no region detected and were priced using a default region:"))
		for _, name := range regionFallbackNames {
			fmt.Printf("    • %s\n", name)
		}
		fmt.Println()
		fmt.Println(mutedSt.Render("  To set a region, use one of:"))
		if regionFallbackProviders["aws"] {
			fmt.Println(mutedSt.Render("    AWS:                  cloudcent pulumi estimate --config aws:region=us-west-2"))
		}
		if regionFallbackProviders["azure"] {
			fmt.Println(mutedSt.Render("    Azure:                cloudcent pulumi estimate --config azure-native:location=eastus"))
		}
		if regionFallbackProviders["gcp"] {
			fmt.Println(mutedSt.Render("    GCP:                  cloudcent pulumi estimate --config gcp:region=us-central1"))
		}
		if regionFallbackProviders["oci"] {
			fmt.Println(mutedSt.Render("    OCI:                  cloudcent pulumi estimate --config oci:region=us-ashburn-1"))
		}
	}

	fmt.Println()
}

func renderPricesTable(prices []resources.PriceEntry) string {
	return renderPricesTableWithUsage(prices, 0)
}

func renderPricesTableWithUsage(prices []resources.PriceEntry, usageQty float64) string {
	// Check if any entry has tiered pricing.
	hasTiers := false
	for _, p := range prices {
		if len(p.Tiers) > 0 {
			hasTiers = true
			break
		}
	}

	if hasTiers {
		return renderTieredPricesTable(prices, usageQty)
	}
	return renderFlatPricesTable(prices)
}

// renderFlatPricesTable renders the standard single-rate pricing table,
// hiding Purchase Option / Term / Upfront columns when all values are empty.
func renderFlatPricesTable(prices []resources.PriceEntry) string {
	// Detect which optional columns have data.
	hasOption, hasTerm, hasUpfront := false, false, false
	for _, p := range prices {
		if p.PurchaseOption != "" {
			hasOption = true
		}
		if p.Term != "" {
			hasTerm = true
		}
		if p.UpfrontFee != "" && p.UpfrontFee != "0" {
			hasUpfront = true
		}
	}

	currentRow := -1
	rows := make([][]string, 0, len(prices))
	for i, p := range prices {
		if p.IsCurrent && currentRow == -1 {
			currentRow = i
		}
		marker := ""
		if p.IsCurrent {
			marker = "▶"
		}
		row := []string{marker, p.Model}
		if hasOption {
			row = append(row, p.PurchaseOption)
		}
		if hasTerm {
			row = append(row, p.Term)
		}
		if hasUpfront {
			upfront := p.UpfrontFee
			if upfront == "" || upfront == "0" {
				upfront = "-"
			}
			row = append(row, upfront)
		}
		row = append(row, p.RatePerHr.String(), p.Unit)
		rows = append(rows, row)
	}

	headers := []string{"", "Model"}
	if hasOption {
		headers = append(headers, "Purchase Option")
	}
	if hasTerm {
		headers = append(headers, "Term")
	}
	if hasUpfront {
		headers = append(headers, "Upfront")
	}
	headers = append(headers, "Price", "Unit")

	headerSt := lipgloss.NewStyle().
		Foreground(colHeader).
		Bold(true).
		Padding(0, 1)
	cellSt := lipgloss.NewStyle().Padding(0, 1)
	currentSt := lipgloss.NewStyle().
		Foreground(colCurrent).
		Bold(true).
		Padding(0, 1)

	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(colBorder)).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerSt
			}
			if row == currentRow {
				return currentSt
			}
			return cellSt
		})

	return indent(t.Render(), "    ")
}

// renderTieredPricesTable renders volume-tiered pricing with one row per tier.
// usageQty > 0 causes all tiers that the usage quantity touches to be highlighted.
func renderTieredPricesTable(prices []resources.PriceEntry, usageQty float64) string {
	headerSt := lipgloss.NewStyle().
		Foreground(colHeader).
		Bold(true).
		Padding(0, 1)
	cellSt := lipgloss.NewStyle().Padding(0, 1)
	currentSt := lipgloss.NewStyle().
		Foreground(colCurrent).
		Bold(true).
		Padding(0, 1)

	// Build a set of row indices that are "active" (touched by usageQty).
	// A tier is active when usageQty > startRange (i.e. some usage falls in
	// this tier or a later one). We walk the OnDemand tiers and mark each
	// tier that receives at least 1 unit.
	activeRows := map[int]bool{}
	if usageQty > 0 {
		qty := decimal.NewFromFloat(usageQty)
		remaining := qty
		rowIdx := 0
		for _, p := range prices {
			if !p.IsCurrent {
				if len(p.Tiers) == 0 {
					rowIdx++
				} else {
					rowIdx += len(p.Tiers)
				}
				continue
			}
			if len(p.Tiers) == 0 {
				activeRows[rowIdx] = true
				rowIdx++
				break
			}
			for _, tier := range p.Tiers {
				if remaining.IsZero() || remaining.IsNegative() {
					rowIdx++
					continue
				}
				start := decimal.Zero
				if tier.StartRange != "" {
					if d, err := decimal.NewFromString(tier.StartRange); err == nil {
						start = d
					}
				}
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
				if units.IsPositive() {
					activeRows[rowIdx] = true
				}
				remaining = remaining.Sub(units)
				rowIdx++
			}
			break
		}
	}

	rows := make([][]string, 0)
	rowIdx := 0

	// Detect optional columns across all entries.
	hasOption, hasTerm := false, false
	for _, p := range prices {
		if p.PurchaseOption != "" {
			hasOption = true
		}
		if p.Term != "" {
			hasTerm = true
		}
	}

	for _, p := range prices {
		isCurrent := p.IsCurrent

		if len(p.Tiers) == 0 {
			marker := ""
			if isCurrent {
				marker = "▶"
			}
			row := []string{marker, p.Model}
			if hasOption {
				row = append(row, p.PurchaseOption)
			}
			if hasTerm {
				row = append(row, p.Term)
			}
			row = append(row, "-", "-", p.RatePerHr.String(), p.Unit)
			rows = append(rows, row)
			rowIdx++
			continue
		}

		for i, tier := range p.Tiers {
			marker := ""
			if isCurrent && activeRows[rowIdx] {
				marker = "▶"
			}
			model := ""
			if i == 0 {
				model = p.Model
			}
			row := []string{marker, model}
			if hasOption {
				opt := ""
				if i == 0 {
					opt = p.PurchaseOption
				}
				row = append(row, opt)
			}
			if hasTerm {
				term := ""
				if i == 0 {
					term = p.Term
				}
				row = append(row, term)
			}
			row = append(row,
				formatRange(tier.StartRange),
				formatRange(tier.EndRange),
				tier.Price,
				p.Unit,
			)
			rows = append(rows, row)
			rowIdx++
		}
	}

	headers := []string{"", "Model"}
	if hasOption {
		headers = append(headers, "Purchase Option")
	}
	if hasTerm {
		headers = append(headers, "Term")
	}
	headers = append(headers, "Start Range", "End Range", "Price", "Unit")

	// StyleFunc highlights all active rows (those touched by usageQty).
	// We need to map table row index back to our rowIdx tracking.
	// Since rows are appended in order, row i in the table = rows[i].
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(colBorder)).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerSt
			}
			if activeRows[row] {
				return currentSt
			}
			return cellSt
		})

	return indent(t.Render(), "    ")
}

// formatRange formats a range value for display, turning "Inf" into "∞"
// and adding thousand separators for large numbers.
func formatRange(s string) string {
	if s == "" {
		return "-"
	}
	if strings.EqualFold(s, "inf") || strings.EqualFold(s, "infinity") {
		return "∞"
	}
	return s
}

// renderTotalsTable renders a summary table in the style of the design mockup:
// each billable resource gets one row (name + sub_label left, pulumi type muted,
// monthly cost right-aligned), with a highlighted "Total Estimated Cost" footer.
func renderTotalsTable(results []resources.EstimateResult, monthlyTotal decimal.Decimal) string {
	colTotalGreen := lipgloss.Color("#00D4AA")
	headerSt := lipgloss.NewStyle().Foreground(colHeader).Bold(true).Padding(0, 1)
	nameSt := lipgloss.NewStyle().Foreground(colTitle).Bold(true).Padding(0, 1)
	typeSt := lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1)
	costSt := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true).Padding(0, 1)
	totalLabelSt := lipgloss.NewStyle().Foreground(colTotalGreen).Bold(true).Padding(0, 1)
	totalCostSt := lipgloss.NewStyle().Foreground(colTotalGreen).Bold(true).Padding(0, 1)
	borderSt := lipgloss.NewStyle().Foreground(colBorder)

	// Build one row per result — billable, free, and unsupported all included.
	type row struct {
		nameCol        string
		typeCol        string
		unitPriceCol   string
		hrsPerMoCol    string
		usageCol       string
		costCol        string
		isFree         bool
		isUnsup        bool
		isTotal        bool
		isDefaultUsage bool
	}
	var rows []row

	for _, r := range results {
		// Left column: resource name, with sub_label appended if present.
		name := r.ResourceName
		if r.SubLabel != "" {
			name = r.ResourceName + " · " + r.SubLabel
		}

		// Middle column: pulumi type from Props["type"], fallback to RawType.
		pulumiType := r.RawType
		if r.Props != nil {
			if t, ok := r.Props["type"]; ok && t != "" {
				pulumiType = t
			}
		}

		if r.StatusMsg != "" {
			isFree := strings.Contains(r.StatusMsg, "free")
			rows = append(rows, row{
				nameCol: name,
				typeCol: pulumiType,
				costCol: r.StatusMsg,
				isFree:  isFree,
				isUnsup: !isFree,
			})
			continue
		}

		// Compute this resource's monthly cost.
		var cost decimal.Decimal
		var usageCol string
		var unitPriceCol string
		var hrsPerMoCol string
		var isDefaultUsage bool
		if r.IsUsageBased {
			cost = r.UsageMonthly
			qty := formatUsageQty(r.UsageQty)
			unit := r.UsageUnit
			if unit == "" {
				unit = "units"
			}
			usageCol = qty + " " + unit + "/mo"
			if r.UsageDefault {
				usageCol += " (default)"
				isDefaultUsage = true
			}
			// For usage-based resources, show the first tier price as unit price
			if len(r.Prices) > 0 {
				for _, p := range r.Prices {
					if p.IsCurrent {
						if len(p.Tiers) > 0 {
							unitPriceCol = "$" + p.Tiers[0].Price
						} else {
							unitPriceCol = "$" + p.RatePerHr.String()
						}
						break
					}
				}
			}
		} else {
			cost = r.EffectiveRate.Mul(decimal.NewFromInt(hoursPerMonth))
			unitPriceCol = "$" + r.EffectiveRate.String() + "/hr"
			hrsPerMoCol = fmt.Sprintf("%d", hoursPerMonth)
		}

		rows = append(rows, row{
			nameCol:        name,
			typeCol:        pulumiType,
			unitPriceCol:   unitPriceCol,
			hrsPerMoCol:    hrsPerMoCol,
			usageCol:       usageCol,
			costCol:        formatDecimal(cost, 2),
			isDefaultUsage: isDefaultUsage,
		})
	}

	// Separator row + Total row.
	rows = append(rows, row{
		nameCol:      "─",
		typeCol:      "─",
		unitPriceCol: "─",
		hrsPerMoCol:  "─",
		usageCol:     "─",
		costCol:      "─",
	})
	rows = append(rows, row{
		nameCol: "Total Estimated Cost",
		typeCol: "",
		costCol: formatDecimal(monthlyTotal, 2),
		isTotal: true,
	})

	// Render using lipgloss table.
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		tableRows[i] = []string{r.nameCol, r.typeCol, r.unitPriceCol, r.hrsPerMoCol, r.usageCol, r.costCol}
	}

	totalRowIdx := len(rows) - 1
	sepRowIdx := len(rows) - 2

	freeSt := lipgloss.NewStyle().Foreground(colFree).Padding(0, 1)
	unsupSt := lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1)
	sepSt := lipgloss.NewStyle().Foreground(colBorder).Padding(0, 1)
	usageSt := lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1)
	usageDefaultSt := lipgloss.NewStyle().Foreground(colWarn).Padding(0, 1)

	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderSt).
		Headers("Resource", "Type", "Unit Price", "Hrs / mo", "Usage", "Est. / mo").
		Rows(tableRows...).
		StyleFunc(func(rowIdx, col int) lipgloss.Style {
			if rowIdx == table.HeaderRow {
				return headerSt
			}
			if rowIdx == sepRowIdx {
				return sepSt
			}
			if rowIdx == totalRowIdx {
				if col == 5 {
					return totalCostSt
				}
				return totalLabelSt
			}
			r := rows[rowIdx]
			if r.isFree {
				if col == 0 {
					return nameSt
				}
				return freeSt
			}
			if r.isUnsup {
				if col == 0 {
					return nameSt
				}
				return unsupSt
			}
			switch col {
			case 0:
				return nameSt
			case 1:
				return typeSt
			case 2, 3:
				return usageSt
			case 4:
				if r.isDefaultUsage {
					return usageDefaultSt
				}
				return usageSt
			default:
				return costSt
			}
		})

	titleLineSt := lipgloss.NewStyle().Foreground(colTotalGreen).Bold(true)
	divider := strings.Repeat("─", 20)
	title := titleLineSt.Render(divider + "  Total Cost Estimation  " + divider)
	return title + "\n" + t.Render()
}

// formatDecimal formats a decimal.Decimal for display, trimming trailing zeros
// but keeping at least `minDecimals` decimal places.
func formatDecimal(d decimal.Decimal, minDecimals int32) string {
	// Use enough precision to show meaningful digits, then trim trailing zeros.
	s := d.StringFixed(minDecimals)
	// Trim trailing zeros after decimal point, but keep at least 2 decimal places.
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		// Ensure at least 2 decimal places for currency readability.
		if idx := strings.Index(s, "."); idx >= 0 && len(s)-idx-1 < 2 {
			for len(s)-strings.Index(s, ".")-1 < 2 {
				s += "0"
			}
		}
	}
	return "$" + s
}

func indent(s, prefix string) string {
	out := ""
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out += prefix + s[start:i+1]
			start = i + 1
		}
	}
	if start < len(s) {
		out += prefix + s[start:]
	}
	return out
}
