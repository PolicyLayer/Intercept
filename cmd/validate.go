package cmd

import (
	"fmt"

	"github.com/policylayer/intercept/internal/config"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate a policy file without starting the proxy",
	RunE:  runValidate,
}

func init() {
	validateCmd.Flags().StringVarP(&cfgPath, "config", "c", "", "path to the policy YAML file (required)")
	validateCmd.MarkFlagRequired("config")
	rootCmd.AddCommand(validateCmd)
}

// runValidate loads and validates a policy file, printing any errors found.
func runValidate(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return err
	}

	errs := config.Validate(cfg)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Printf("Error: %v\n", e)
		}
		return fmt.Errorf("policy validation failed with %d error(s)", len(errs))
	}

	fmt.Println("Policy file is valid.")
	return nil
}
