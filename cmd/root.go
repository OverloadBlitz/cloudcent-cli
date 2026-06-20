package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const Version = "0.0.3-beta"

var rootCmd = &cobra.Command{
	Use:     "cloudcent",
	Version: Version,
	Short:   "CloudCent — cloud pricing CLI",
	Long:    "CloudCent is a CLI for estimating cloud costs with pulumi",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// Execute is the entry point called from main.
//
// Exit code convention:
//
//	0 — success / guardrail passed
//	1 — runtime error (build, auth, network, bad input)
//	2 — a cost guardrail threshold was breached
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var breach *guardrailBreachError
		if errors.As(err, &breach) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(
		initCmd,
		pricingCmd,
		historyCmd,
		cacheCmd,
		configCmd,
		metadataCmd,
		pulumiCmd,
		uiCmd,
	)
}
