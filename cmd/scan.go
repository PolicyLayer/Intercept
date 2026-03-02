package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/policylayer/intercept/internal/scan"
	"github.com/spf13/cobra"
)

var (
	scanUpstream string
	scanHeaders  []string
	scanOutput   string
	scanTimeout  time.Duration
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Connect to an MCP server and generate a policy scaffold",
	Long: `Scan connects to an MCP server, discovers all available tools, and generates
a well-commented policy YAML file that you can fill in with rules.

Examples:
  intercept scan -- npx @modelcontextprotocol/server-github
  intercept scan --upstream https://mcp.example.com/sse
  intercept scan --upstream https://mcp.stripe.com --header "Authorization: Bearer tok"
  intercept scan -o policy.yaml -- npx server-github`,
	Args: cobra.ArbitraryArgs,
	RunE: runScan,
}

func init() {
	scanCmd.Flags().StringVar(&scanUpstream, "upstream", "", "upstream server URL")
	scanCmd.Flags().StringArrayVar(&scanHeaders, "header", nil, "custom headers for upstream requests (e.g. \"Authorization: Bearer tok\")")
	scanCmd.Flags().StringVarP(&scanOutput, "output", "o", "", "write output to file instead of stdout")
	scanCmd.Flags().DurationVar(&scanTimeout, "timeout", 30*time.Second, "max time to wait for server startup and tool listing")
	rootCmd.AddCommand(scanCmd)
}

// runScan connects to an MCP server (stdio or HTTP), discovers available tools,
// and writes a policy scaffold YAML to stdout or a file.
func runScan(cmd *cobra.Command, args []string) error {
	setupLogger()

	dashIdx := cmd.ArgsLenAtDash()
	hasUpstreamCmd := dashIdx >= 0 && len(args[dashIdx:]) > 0
	hasUpstreamURL := scanUpstream != ""

	if hasUpstreamCmd && hasUpstreamURL {
		return fmt.Errorf("cannot specify both upstream command (after --) and --upstream URL")
	}
	if !hasUpstreamCmd && !hasUpstreamURL {
		return fmt.Errorf("specify either a command after '--' or --upstream URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()

	var tools []scan.MCPTool
	var serverName string

	switch {
	case hasUpstreamCmd:
		upstreamCmd := args[dashIdx:]
		serverName = strings.Join(upstreamCmd, " ")

		slog.Debug("scanning stdio server", "command", upstreamCmd)

		child := exec.CommandContext(ctx, upstreamCmd[0], upstreamCmd[1:]...)
		child.Stderr = os.Stderr

		childIn, err := child.StdinPipe()
		if err != nil {
			return fmt.Errorf("creating stdin pipe: %w", err)
		}
		childOut, err := child.StdoutPipe()
		if err != nil {
			return fmt.Errorf("creating stdout pipe: %w", err)
		}

		if err := child.Start(); err != nil {
			return fmt.Errorf("starting server: %w", err)
		}

		tools, err = scan.ListTools(ctx, childIn, childOut)

		// Clean up the child process before checking err. The process is
		// killed unconditionally since we only needed it for tool discovery.
		childIn.Close()
		if child.Process != nil {
			child.Process.Kill()
		}
		child.Wait() //nolint:errcheck // expected non-zero exit from kill

		if err != nil {
			return fmt.Errorf("listing tools: %w", err)
		}

	case hasUpstreamURL:
		serverName = scanUpstream

		slog.Debug("scanning HTTP server", "url", scanUpstream)

		hdrs, err := parseHeaders(scanHeaders)
		if err != nil {
			return err
		}
		tools, err = scan.ListToolsHTTP(ctx, scanUpstream, hdrs)
		if err != nil {
			return fmt.Errorf("listing tools: %w", err)
		}
	}

	return writeOutput(tools, serverName)
}

// writeOutput generates policy YAML from the discovered tools and writes it
// to the file specified by --output, or to stdout if no output path is set.
func writeOutput(tools []scan.MCPTool, serverName string) error {
	out, err := scan.GenerateYAML(tools, serverName)
	if err != nil {
		return fmt.Errorf("generating YAML: %w", err)
	}

	if scanOutput != "" {
		dir := filepath.Dir(scanOutput)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating output directory: %w", err)
		}
		if err := os.WriteFile(scanOutput, out, 0o644); err != nil {
			return fmt.Errorf("writing output file: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Policy scaffold written to %s (%d tools)\n", scanOutput, len(tools))
		return nil
	}

	_, err = os.Stdout.Write(out)
	return err
}
