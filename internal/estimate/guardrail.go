package estimate

import (
	"fmt"
	"strconv"
)

// GuardrailConfig holds the thresholds a cost estimate is judged against. All
// fields are optional pointers: a nil pointer means "this check is disabled".
// When every field is nil the guardrail is purely informational and always
// passes.
type GuardrailConfig struct {
	// Budget is the absolute monthly ceiling in USD. The estimate breaches when
	// its monthly total exceeds this value.
	Budget *float64
	// Baseline is the monthly total (USD) of the comparison point — typically the
	// base branch's estimate. Setting it enables the relative-increase checks.
	Baseline *float64
	// MaxIncrease is the largest allowed absolute increase in USD over Baseline.
	MaxIncrease *float64
	// MaxIncreasePct is the largest allowed percentage increase over Baseline.
	MaxIncreasePct *float64
}

// Enabled reports whether any threshold is configured. When false the guardrail
// only reports numbers and never fails.
func (c GuardrailConfig) Enabled() bool {
	return c.Budget != nil || c.MaxIncrease != nil || c.MaxIncreasePct != nil
}

// GuardrailResult is the machine-readable verdict embedded in JSON output and
// consumed by CI to decide pass/fail.
type GuardrailResult struct {
	Passed       bool     `json:"passed"`
	MonthlyTotal float64  `json:"monthly_total"`
	Budget       *float64 `json:"budget,omitempty"`
	Baseline     *float64 `json:"baseline,omitempty"`
	Delta        *float64 `json:"delta,omitempty"`
	DeltaPct     *float64 `json:"delta_pct,omitempty"`
	// Breaches lists human-readable reasons the guardrail failed. Empty when passed.
	Breaches []string `json:"breaches,omitempty"`
}

// EvaluateGuardrail compares the computed totals against the configured
// thresholds and returns a verdict. The monthly total is parsed from
// totals.MonthlyTotal (the same string that is printed), so the judged number
// always matches the displayed number.
func EvaluateGuardrail(totals JSONTotals, cfg GuardrailConfig) GuardrailResult {
	monthly, _ := strconv.ParseFloat(totals.MonthlyTotal, 64)

	res := GuardrailResult{
		Passed:       true,
		MonthlyTotal: monthly,
		Budget:       cfg.Budget,
		Baseline:     cfg.Baseline,
	}

	// Absolute budget cap.
	if cfg.Budget != nil && monthly > *cfg.Budget {
		res.Passed = false
		res.Breaches = append(res.Breaches,
			fmt.Sprintf("monthly cost $%.2f exceeds budget $%.2f", monthly, *cfg.Budget))
	}

	// Relative checks against the baseline.
	if cfg.Baseline != nil {
		baseline := *cfg.Baseline
		delta := monthly - baseline
		res.Delta = &delta

		var pct float64
		if baseline != 0 {
			pct = delta / baseline * 100
			res.DeltaPct = &pct
		}

		if cfg.MaxIncrease != nil && delta > *cfg.MaxIncrease {
			res.Passed = false
			res.Breaches = append(res.Breaches,
				fmt.Sprintf("cost increase $%.2f exceeds max increase $%.2f (baseline $%.2f → $%.2f)",
					delta, *cfg.MaxIncrease, baseline, monthly))
		}

		if cfg.MaxIncreasePct != nil {
			switch {
			case baseline == 0 && delta > 0:
				// Any positive increase from a zero baseline is an infinite percentage.
				res.Passed = false
				res.Breaches = append(res.Breaches,
					fmt.Sprintf("cost increased from $0.00 to $%.2f, exceeding max increase of %.1f%%",
						monthly, *cfg.MaxIncreasePct))
			case baseline != 0 && pct > *cfg.MaxIncreasePct:
				res.Passed = false
				res.Breaches = append(res.Breaches,
					fmt.Sprintf("cost increase %.1f%% exceeds max increase %.1f%% (baseline $%.2f → $%.2f)",
						pct, *cfg.MaxIncreasePct, baseline, monthly))
			}
		}
	}

	return res
}
