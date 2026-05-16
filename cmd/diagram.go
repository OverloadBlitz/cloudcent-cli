package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/api"
	"github.com/OverloadBlitz/cloudcent-cli/internal/drawio"
	"github.com/OverloadBlitz/cloudcent-cli/internal/estimate"
	"github.com/spf13/cobra"
)

var (
	diagramInitSpec           string
	diagramInitRegion         string
	diagramInitForce          bool
	diagramEstimateSpec       string
	diagramEstimateUsageFlags []string
	diagramEstimateModelFlags []string
	diagramEstimateOutput     string
)

var diagramCmd = &cobra.Command{
	Use:   "diagram",
	Short: "Parse draw.io diagrams and estimate cloud costs",
	Long: `Work with draw.io diagrams: detect cloud components, scaffold a YAML
spec for them, and estimate costs from that spec.

Running ` + "`cloudcent diagram <file>`" + ` is a shortcut for ` + "`cloudcent diagram parse <file>`" + `.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return runDiagramParse(cmd, args)
	},
}

var diagramParseCmd = &cobra.Command{
	Use:   "parse <path>",
	Short: "Parse a draw.io diagram and detect cloud components",
	Args:  cobra.ExactArgs(1),
	RunE:  runDiagramParse,
}

var diagramInitCmd = &cobra.Command{
	Use:   "init <path>",
	Short: "Write a YAML spec template alongside the diagram for cost estimation",
	Args:  cobra.ExactArgs(1),
	RunE:  runDiagramInitCmd,
}

var diagramEstimateCmd = &cobra.Command{
	Use:   "estimate <path>",
	Short: "Estimate costs using the YAML spec next to the diagram",
	Args:  cobra.ExactArgs(1),
	RunE:  runDiagramEstimateCmd,
}

func init() {
	diagramInitCmd.Flags().StringVar(&diagramInitSpec, "spec", "", "Path to write the YAML spec (default: <diagram>.cloudcent.yaml)")
	diagramInitCmd.Flags().StringVar(&diagramInitRegion, "region", "us-east-1", "Default region used when seeding the spec")
	diagramInitCmd.Flags().BoolVar(&diagramInitForce, "force", false, "Overwrite an existing spec file")

	diagramEstimateCmd.Flags().StringVar(&diagramEstimateSpec, "spec", "", "Path to the YAML spec file (default: <diagram>.cloudcent.yaml)")
	diagramEstimateCmd.Flags().StringArrayVar(&diagramEstimateUsageFlags, "usage", nil, "Monthly usage for a usage-based resource, e.g. --usage my-api=5000000 (can be repeated)")
	diagramEstimateCmd.Flags().StringArrayVar(&diagramEstimateModelFlags, "model", nil, "Pricing model override, e.g. --model \"Reserved:standard:1yr\" or --model \"my-ec2=spot\" (can be repeated)")
	diagramEstimateCmd.Flags().StringVarP(&diagramEstimateOutput, "output", "o", "table", "Output format: table or json")

	diagramCmd.AddCommand(diagramParseCmd, diagramInitCmd, diagramEstimateCmd)
}

func runDiagramParse(cmd *cobra.Command, args []string) error {
	path := args[0]
	d, err := drawio.ParseFile(path)
	if err != nil {
		return err
	}

	if len(d.Components) == 0 {
		fmt.Printf("No cloud components detected in %s\n", path)
		return nil
	}

	groups := groupByProduct(d.Components)
	fmt.Printf("Detected %d component(s) in %s across %d product(s)\n\n",
		len(d.Components), path, len(groups))

	for i, g := range groups {
		fmt.Printf("  %d. %s [%s]\n", i+1, g.Product, formatLabels(g.Labels))
	}
	fmt.Println()
	return nil
}

func runDiagramInitCmd(cmd *cobra.Command, args []string) error {
	diagramPath := args[0]
	d, err := drawio.ParseFile(diagramPath)
	if err != nil {
		return err
	}
	return runDiagramInit(diagramPath, d)
}

func runDiagramEstimateCmd(cmd *cobra.Command, args []string) error {
	diagramPath := args[0]
	d, err := drawio.ParseFile(diagramPath)
	if err != nil {
		return err
	}
	return runDiagramEstimate(diagramPath, d)
}

func runDiagramInit(diagramPath string, d *drawio.Diagram) error {
	if len(d.Components) == 0 {
		return fmt.Errorf("no cloud components detected in %s — nothing to write", diagramPath)
	}

	specPath := diagramInitSpec
	if specPath == "" {
		specPath = drawio.DefaultSpecPath(diagramPath)
	}

	if _, err := os.Stat(specPath); err == nil && !diagramInitForce {
		return fmt.Errorf("spec file already exists at %s — edit it directly, or pass --force to overwrite", specPath)
	}

	meta, metaErr := api.LoadMetadataFromFile()
	if metaErr != nil {
		fmt.Printf("Note: pricing metadata not available (%v); spec will lack example values. Run `cloudcent metadata refresh` for richer suggestions.\n", metaErr)
		meta = nil
	}

	spec := drawio.GenerateSpec(d, meta, diagramInitRegion)

	f, err := os.Create(specPath)
	if err != nil {
		return fmt.Errorf("creating spec file: %w", err)
	}
	defer f.Close()

	if err := drawio.WriteSpec(f, spec, meta); err != nil {
		return fmt.Errorf("writing spec: %w", err)
	}

	billable := 0
	for _, c := range spec.Components {
		if !c.NoPricing {
			billable++
		}
	}
	fmt.Printf("Wrote %d component(s) (%d billable) to %s\n", len(spec.Components), billable, specPath)
	fmt.Printf("Edit the attrs in the spec, then run `cloudcent diagram estimate %s` to price it.\n", diagramPath)
	return nil
}

func runDiagramEstimate(diagramPath string, d *drawio.Diagram) error {
	specPath := diagramEstimateSpec
	if specPath == "" {
		specPath = drawio.DefaultSpecPath(diagramPath)
	}

	spec, err := drawio.LoadSpec(specPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("spec file not found at %s — run `cloudcent diagram init %s` first", specPath, diagramPath)
		}
		return err
	}

	meta, metaErr := api.LoadMetadataFromFile()
	if metaErr != nil {
		fmt.Printf("Note: pricing metadata not available (%v); attr validation will be skipped.\n", metaErr)
		meta = nil
	}

	decoded, validationErrs := drawio.SpecToDecoded(spec, meta)
	if len(validationErrs) > 0 {
		fmt.Println("Spec validation errors:")
		for _, e := range validationErrs {
			fmt.Printf("  - %v\n", e)
		}
		return fmt.Errorf("fix the spec at %s and retry", specPath)
	}
	if len(decoded) == 0 {
		fmt.Println("No billable resources in the spec.")
		return nil
	}

	client, err := api.New()
	if err != nil {
		return fmt.Errorf("pricing api error: %w", err)
	}

	// Apply region fallback for resources without a region.
	// Skip free/no-pricing resources — they don't need region for pricing.
	for i := range decoded {
		if !decoded[i].NoPricing && strings.TrimSpace(decoded[i].Region) == "" {
			decoded[i].Region = "us-east-1"
			decoded[i].RegionFallback = true
		}
	}

	usageMap := parseUsageFlags(diagramEstimateUsageFlags)

	modelMap := parseModelFlags(diagramEstimateModelFlags)

	fmt.Printf("\n=== Resources to be priced: %d ===\n", len(decoded))
	results, err := estimate.EstimateAllResources(client, decoded, usageMap, modelMap)
	if err != nil {
		return fmt.Errorf("estimating resources: %w", err)
	}

	if diagramEstimateOutput == "json" {
		estimate.PrintResultsJSON(results)
	} else {
		estimate.PrintResults(results)
	}
	_ = d
	return nil
}

// productGroup buckets all components that share the same `provider:service`
// tag (the suffix carried by the resIcon/grIcon/shape style attribute).
// Used by `diagram parse` to invert the per-component listing into a
// per-product summary.
type productGroup struct {
	// Product is the display key, e.g. `aws:elastic_load_balancing`,
	// `azure:virtual_machine`, or `Unknown` when the shape couldn't be
	// identified at all.
	Product string
	// Labels is the list of draw.io cell labels in this group, in the order
	// they appear in the diagram.
	Labels []string
}

// groupByProduct collapses the component slice into one entry per
// `provider:service` tag. Order: groups appear in the order their first
// component is encountered in the diagram, so the listing roughly mirrors
// the visual top-to-bottom layout.
func groupByProduct(comps []drawio.Component) []productGroup {
	idx := map[string]int{}
	var groups []productGroup

	for _, c := range comps {
		key := serviceKey(c)
		i, ok := idx[key]
		if !ok {
			groups = append(groups, productGroup{Product: key})
			i = len(groups) - 1
			idx[key] = i
		}
		groups[i].Labels = append(groups[i].Labels, labelOrID(c))
	}
	return groups
}

// serviceKey returns the `provider:service` tag for a component, or
// "Unknown" if no shape suffix was extracted at all.
func serviceKey(c drawio.Component) string {
	if c.ServiceType == "" {
		return "Unknown"
	}
	if c.Provider == "" {
		return c.ServiceType
	}
	return c.Provider + ":" + c.ServiceType
}

// labelOrID returns the draw.io cell label, falling back to the cell ID if
// the label is empty (rare — Parse already drops vertices with empty labels,
// but kept defensively for tests / future inputs).
func labelOrID(c drawio.Component) string {
	if strings.TrimSpace(c.Label) != "" {
		return c.Label
	}
	return c.ID
}

// formatLabels collapses repeated identical labels into `Label ×N` while
// preserving the order each unique label was first seen. Single
// occurrences keep their bare name.
//
// Example:
//
//	["M4", "M4", "M4", "M4"]                -> "M4 ×4"
//	["RDS Master", "RDS Slave"]             -> "RDS Master, RDS Slave"
//	["ELB", "ELB", "Other ELB"]             -> "ELB ×2, Other ELB"
func formatLabels(labels []string) string {
	counts := map[string]int{}
	order := []string{}
	for _, l := range labels {
		if _, seen := counts[l]; !seen {
			order = append(order, l)
		}
		counts[l]++
	}
	parts := make([]string, 0, len(order))
	for _, l := range order {
		if counts[l] > 1 {
			parts = append(parts, fmt.Sprintf("%s ×%d", l, counts[l]))
		} else {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, ", ")
}
