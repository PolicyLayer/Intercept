// Package cmd defines the Cobra command tree for the intercept CLI. The root
// command starts the proxy; subcommands provide scan, validate, and status.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/policylayer/intercept/internal/config"
	"github.com/policylayer/intercept/internal/engine"
	"github.com/policylayer/intercept/internal/events"
	"github.com/policylayer/intercept/internal/proxy"
	"github.com/policylayer/intercept/internal/reload"
	"github.com/policylayer/intercept/internal/state"
	"github.com/policylayer/intercept/internal/transport"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	cfgPath       string
	stateDir      string
	stateDSN      string
	statePrefix   string
	stateFailMode string
	logLevel      string
	transportMode string
	upstream      string
	headers       []string
	bind          string
	port          int
)

var rootCmd = &cobra.Command{
	Use:   "intercept",
	Short: "MCP policy enforcement proxy",
	Long: `Intercept is a transparent proxy that enforces configurable policy rules on MCP tool calls.

There are three ways to run intercept:

  1. Wrap a local server:       intercept -c policy.yaml -- <command>
  2. Bridge to a remote server: intercept -c policy.yaml --upstream <url>
  3. HTTP proxy:                intercept -c policy.yaml --transport http --upstream <url>

Use 'intercept scan' to discover tools and generate a policy scaffold.`,
	Example: `  # Wrap a local MCP server with policy enforcement (stdin/stdout)
  intercept -c policy.yaml -- npx -y @modelcontextprotocol/server-github

  # Bridge stdin/stdout to a remote MCP server
  intercept -c policy.yaml --upstream https://mcp.stripe.com

  # Run an HTTP proxy to a remote MCP server
  intercept -c policy.yaml --transport http --upstream https://mcp.stripe.com --port 8080`,
	Args:         cobra.ArbitraryArgs,
	SilenceUsage: true,
	RunE:         runRoot,
}

func init() {
	rootCmd.Flags().StringVarP(&cfgPath, "config", "c", "", "path to the policy YAML file (required)")
	rootCmd.MarkFlagRequired("config")

	home, _ := os.UserHomeDir()
	defaultStateDir := filepath.Join(home, ".intercept", "state")

	rootCmd.Flags().StringVar(&stateDir, "state-dir", defaultStateDir, "directory for persistent state")
	rootCmd.Flags().StringVar(&stateDSN, "state-dsn", "", "shared state backend DSN (e.g. redis://host:6379/0)")
	rootCmd.Flags().StringVar(&statePrefix, "state-prefix", "", "key prefix for state counters (e.g. stripe-prod:)")
	rootCmd.Flags().StringVar(&stateFailMode, "state-fail-mode", "closed", "behavior when state backend is unreachable: open or closed")
	rootCmd.MarkFlagsMutuallyExclusive("state-dir", "state-dsn")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "info", "logging verbosity: debug, info, warn, error")
	rootCmd.Flags().StringVar(&transportMode, "transport", "", "transport mode: stdio, sse, http")
	rootCmd.Flags().StringVar(&upstream, "upstream", "", "upstream server URL")
	rootCmd.Flags().StringArrayVar(&headers, "header", nil, "custom headers for upstream requests (e.g. \"Authorization: Bearer tok\")")
	rootCmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "bind address for HTTP/SSE listener")
	rootCmd.Flags().IntVar(&port, "port", 0, "port for HTTP/SSE listener (0 for auto-assign)")

	// Annotate flags with groups for organized help output.
	rootCmd.Flags().SetAnnotation("state-dir", "group", []string{"state"})
	rootCmd.Flags().SetAnnotation("state-dsn", "group", []string{"state"})
	rootCmd.Flags().SetAnnotation("state-prefix", "group", []string{"state"})
	rootCmd.Flags().SetAnnotation("state-fail-mode", "group", []string{"state"})

	rootCmd.Flags().SetAnnotation("upstream", "group", []string{"remote"})
	rootCmd.Flags().SetAnnotation("header", "group", []string{"remote"})

	rootCmd.Flags().SetAnnotation("transport", "group", []string{"http-proxy"})
	rootCmd.Flags().SetAnnotation("bind", "group", []string{"http-proxy"})
	rootCmd.Flags().SetAnnotation("port", "group", []string{"http-proxy"})

	cobra.AddTemplateFunc("groupedFlagUsages", groupedFlagUsages)
	rootCmd.SetUsageTemplate(usageTemplate)
}

// SetVersion configures the version string shown by --version.
func SetVersion(version, commit string) {
	rootCmd.Version = version + " (" + commit + ")"
}

// PrintHelp prints the root command's help text.
func PrintHelp() {
	rootCmd.Help()
}

// Execute runs the root command. It is the single entry point called by main.
func Execute() error {
	return rootCmd.Execute()
}

// runRoot is the main entrypoint for the proxy. It loads config, selects a
// transport, opens the state store, and starts proxying traffic.
func runRoot(cmd *cobra.Command, args []string) error {
	setupLogger()

	// Determine transport mode based on flags and arguments.
	dashIdx := cmd.ArgsLenAtDash()
	hasUpstreamCmd := dashIdx >= 0 && len(args[dashIdx:]) > 0
	hasUpstreamURL := upstream != ""

	// Cross-flag validation (before config load for fast feedback).
	if (cmd.Flags().Changed("bind") || cmd.Flags().Changed("port")) && transportMode != "http" && transportMode != "sse" {
		return fmt.Errorf("--bind and --port require --transport http or --transport sse")
	}
	if len(headers) > 0 && upstream == "" {
		return fmt.Errorf("--header requires --upstream")
	}
	if (transportMode == "http" || transportMode == "sse") && upstream == "" {
		return fmt.Errorf("--transport %s requires --upstream URL", transportMode)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	errs := config.Validate(cfg)
	if len(errs) > 0 {
		for _, e := range errs {
			slog.Error("config validation", "error", e)
		}
		return fmt.Errorf("config validation failed with %d error(s)", len(errs))
	}

	slog.Debug("config loaded",
		"tools", len(cfg.Tools),
		"description", cfg.Description,
	)

	store, err := state.OpenStore(stateDSN, stateDir, statePrefix, stateFailMode)
	if err != nil {
		return fmt.Errorf("opening state store: %w", err)
	}

	eventsDir, err := interceptSubdir("events")
	if err != nil {
		store.Close()
		return fmt.Errorf("resolving events directory: %w", err)
	}

	if err := events.PruneOldFiles(eventsDir); err != nil {
		slog.Warn("event cleanup failed", "error", err)
	}

	instanceID := events.NewInstanceID()
	emitter, err := events.NewJSONLEmitter(eventsDir, instanceID)
	if err != nil {
		store.Close()
		return fmt.Errorf("creating event emitter: %w", err)
	}

	stateBackend := "sqlite"
	if stateDSN != "" {
		stateBackend = stateDSN
	}

	eng := engine.New(cfg, store)
	h := proxy.New(eng, store, cfg, emitter)
	filter := h.ToolListFilter()

	var tr transport.Transport
	var serverName string

	// Validate explicit --transport against the provided flags.
	if transportMode == "stdio" && hasUpstreamCmd && hasUpstreamURL {
		return fmt.Errorf("cannot specify both upstream command (after --) and --upstream URL")
	}
	if (transportMode == "sse" || transportMode == "http") && hasUpstreamCmd {
		return fmt.Errorf("upstream command (after --) cannot be used with --transport %s", transportMode)
	}

	switch {
	// stdio with upstream command (e.g. intercept -- npx server-github)
	case hasUpstreamCmd && !hasUpstreamURL && (transportMode == "" || transportMode == "stdio"):
		upstreamCmd := args[dashIdx:]
		slog.Debug("upstream command", "args", upstreamCmd)
		tr = transport.NewStdioTransport(upstreamCmd, filter)
		serverName = filepath.Base(upstreamCmd[0])

	// HTTP/SSE proxy (e.g. intercept --upstream https://mcp.stripe.com --transport http)
	case (transportMode == "sse" || transportMode == "http") && hasUpstreamURL:
		slog.Debug("upstream URL", "url", upstream, "transport", transportMode)
		tr = &transport.HTTPTransport{
			Upstream:       upstream,
			Bind:           bind,
			Port:           port,
			Stderr:         os.Stderr,
			TransportMode:  transportMode,
			ToolListFilter: filter,
		}
		serverName = upstream

	// stdio bridge to upstream URL (e.g. intercept --upstream https://mcp.stripe.com)
	case hasUpstreamURL && !hasUpstreamCmd && (transportMode == "" || transportMode == "stdio"):
		hdrs, err := parseHeaders(headers)
		if err != nil {
			return err
		}
		slog.Debug("stdio proxy to upstream URL", "url", upstream)
		tr = transport.NewStdioBridgeTransport(upstream, hdrs, filter)
		serverName = upstream

	case hasUpstreamCmd && hasUpstreamURL:
		return fmt.Errorf("cannot specify both upstream command (after --) and --upstream URL")

	default:
		return fmt.Errorf("specify either a command after '--' or --upstream URL\nRun 'intercept --help' for usage examples.")
	}

	emitter.Emit(events.Event{
		Type:         "startup",
		Server:       serverName,
		Config:       cfgPath,
		PID:          os.Getpid(),
		StateBackend: stateBackend,
		FailMode:     stateFailMode,
	})

	var currentCfg atomic.Pointer[config.Config]
	currentCfg.Store(cfg)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		swap := func(newCfg *config.Config) {
			eng.SetConfig(newCfg)
			h.SetConfig(newCfg)
			currentCfg.Store(newCfg)
			emitter.Emit(events.Event{Type: "config_reload", Status: "success"})
		}
		onReloadError := func(err error) {
			emitter.Emit(events.Event{Type: "config_reload", Status: "failure", Message: err.Error()})
		}
		if err := reload.Watch(ctx, cfgPath, swap, onReloadError); err != nil {
			slog.Error("config watcher failed", "error", err)
		}
	}()

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c := currentCfg.Load()
				counters := make(map[string]int64)
				for toolName, tool := range c.Tools {
					scope := toolName
					if toolName == "*" {
						scope = "_global"
					}
					for _, rule := range tool.Rules {
						if rule.State != nil {
							key := scope + "." + rule.State.Counter
							val, _, err := store.Get(key)
							if err == nil {
								counters[key] = val
							}
						}
					}
				}
				emitter.Emit(events.Event{Type: "heartbeat", Counters: counters})
			}
		}
	}()

	err = tr.Start(ctx, h.Handle)
	cancel()

	emitter.Emit(events.Event{Type: "shutdown"})
	emitter.Close()
	store.Close()

	return err
}

// parseHeaders converts "Key: Value" strings into a map.
func parseHeaders(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(raw))
	for _, h := range raw {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			return nil, fmt.Errorf("invalid header (expected \"Name: Value\"): %s", h)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

// interceptSubdir returns the path to a subdirectory under ~/.intercept.
func interceptSubdir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".intercept", name), nil
}

// groupedFlagUsages renders flags organized by their "group" annotation.
func groupedFlagUsages(fs *pflag.FlagSet) string {
	groupLabels := map[string]string{
		"":           "Flags",
		"state":      "State Flags",
		"remote":     "Remote Upstream Flags",
		"http-proxy": "HTTP Proxy Flags",
	}
	groupOrder := []string{"", "state", "remote", "http-proxy"}

	buckets := make(map[string]*pflag.FlagSet)
	for _, g := range groupOrder {
		buckets[g] = pflag.NewFlagSet(g, pflag.ContinueOnError)
	}

	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Deprecated != "" {
			return
		}
		group := ""
		if ann, ok := f.Annotations["group"]; ok && len(ann) > 0 {
			group = ann[0]
		}
		if bucket, ok := buckets[group]; ok {
			bucket.AddFlag(f)
		} else {
			buckets[""].AddFlag(f)
		}
	})

	var b strings.Builder
	for _, g := range groupOrder {
		usage := buckets[g].FlagUsages()
		if usage == "" {
			continue
		}
		b.WriteString(groupLabels[g])
		b.WriteString(":\n")
		b.WriteString(usage)
		b.WriteString("\n")
	}
	return b.String()
}

// Based on cobra v1.10.2's default usage template, with groupedFlagUsages
// swapped in for the local flags section.
var usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

{{ groupedFlagUsages .LocalFlags }}{{end}}{{if .HasAvailableInheritedFlags}}
Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// setupLogger configures the global slog logger based on the --log-level flag.
func setupLogger() {
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}
