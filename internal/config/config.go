// Package config handles loading, parsing, and defaulting of policy YAML files.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level policy configuration.
type Config struct {
	Version     string             `yaml:"version"`
	Description string             `yaml:"description"`
	Default     string             `yaml:"default"`
	Tools       map[string]ToolDef `yaml:"tools"`
}

// ToolDef defines the rules for a single tool.
type ToolDef struct {
	Rules []Rule `yaml:"rules"`
}

// Rule defines a single policy rule.
type Rule struct {
	Name       string      `yaml:"name"`
	Action     string      `yaml:"action"`
	RateLimit  string      `yaml:"rate_limit"`
	Conditions []Condition `yaml:"conditions"`
	OnDeny     string      `yaml:"on_deny"`
	State      *StateDef   `yaml:"state"`
}

// Condition defines a single condition within a rule.
type Condition struct {
	Path  string `yaml:"path"`
	Op    string `yaml:"op"`
	Value any    `yaml:"value"`
}

// StateDef defines stateful counter tracking for a rule.
type StateDef struct {
	Counter       string `yaml:"counter"`
	Window        string `yaml:"window"`
	Increment     int    `yaml:"increment"`
	IncrementFrom string `yaml:"increment_from"`
}

// Load reads and parses a YAML policy file from the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

// applyDefaults fills in omitted fields and desugars the rate_limit shorthand
// into the full conditions + state representation.
func applyDefaults(cfg *Config) {
	if cfg.Default == "" {
		cfg.Default = "allow"
	}

	for toolName, tool := range cfg.Tools {
		for i := range tool.Rules {
			r := &tool.Rules[i]

			// Desugar rate_limit shorthand before other defaults.
			if r.RateLimit != "" {
				canDesugar := len(r.Conditions) == 0 && r.State == nil &&
					r.Action != "deny"
				if canDesugar {
					count, window, err := parseRateLimit(r.RateLimit)
					if err == nil {
						scope := toolName
						if scope == "*" {
							scope = "_global"
						}
						counterName := "_rate_" + window
						r.Action = "evaluate"
						r.Conditions = []Condition{{
							Path:  fmt.Sprintf("state.%s.%s", scope, counterName),
							Op:    "lte",
							Value: count,
						}}
						r.State = &StateDef{
							Counter:   counterName,
							Window:    window,
							Increment: 1,
						}
						if r.OnDeny == "" {
							r.OnDeny = fmt.Sprintf(
								"Rate limit of %d per %s reached. Try again later.",
								count, window,
							)
						}
						r.RateLimit = ""
					}
				}
			}

			if r.Action == "" {
				r.Action = "evaluate"
			}
			if r.State != nil && r.State.Increment == 0 {
				r.State.Increment = 1
			}
		}
		cfg.Tools[toolName] = tool
	}
}

// parseRateLimit parses a string like "5/hour" into count and window.
func parseRateLimit(s string) (count int, window string, err error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("rate_limit must be in the format \"N/window\" (e.g. \"5/hour\"), got %q", s)
	}
	count, err = strconv.Atoi(parts[0])
	if err != nil || count <= 0 {
		return 0, "", fmt.Errorf("rate_limit count must be a positive integer, got %q", parts[0])
	}
	window = parts[1]
	if !validWindows[window] {
		return 0, "", fmt.Errorf("rate_limit window must be \"minute\", \"hour\", or \"day\", got %q", window)
	}
	return count, window, nil
}
