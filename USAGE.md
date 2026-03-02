# CLI Reference

For the policy YAML format, see [POLICY.md](POLICY.md).

## Proxy command

The root command runs Intercept as a transparent proxy in front of an MCP server, enforcing [policy rules](POLICY.md) on every tool call.

```
intercept -c <policy-file> [flags] -- <upstream-command...>
intercept -c <policy-file> [flags] --upstream <url>
```

### Flags

| Flag | Short | Type | Default | Description |
|---|---|---|---|---|
| `--config` | `-c` | string | (required) | Path to the [policy YAML file](POLICY.md) |
| `--state-dir` | | string | `~/.intercept/state` | Directory for persistent state (SQLite database) |
| `--state-dsn` | | string | | Shared state backend DSN (e.g. `redis://host:6379/0`) |
| `--state-prefix` | | string | | Key prefix for state counters (e.g. `stripe-prod:`) |
| `--state-fail-mode` | | string | `closed` | Behavior when state backend is unreachable: `open` or `closed` |
| `--log-level` | | string | `info` | Logging verbosity: `debug`, `info`, `warn`, `error` |
| `--transport` | | string | (auto-detect) | Transport mode: `stdio`, `sse`, `http` |
| `--upstream` | | string | | Upstream server URL |
| `--header` | | string[] | | Custom headers for upstream requests (e.g. `"Authorization: Bearer tok"`) |
| `--bind` | | string | `127.0.0.1` | Bind address for HTTP/SSE listener |
| `--port` | | int | `0` (auto) | Port for HTTP/SSE listener |

`--state-dir` and `--state-dsn` are mutually exclusive.

### Transport modes

Intercept supports three transport modes. The mode is auto-detected from the flags provided, or can be set explicitly with `--transport`.

**Stdio with child process** (activates when a command follows `--`):

Intercept launches the upstream MCP server as a child process and proxies stdin/stdout. JSON-RPC messages are intercepted and evaluated. Signals (SIGTERM, SIGINT) are forwarded to the child process. If the child exits, Intercept exits with the same status.

```sh
intercept -c policy.yaml -- npx -y @modelcontextprotocol/server-github
```

**Stdio with upstream URL** (default when `--upstream` is provided):

Intercept reads JSON-RPC from stdin and forwards requests over HTTP to the upstream MCP server. Responses are written back to stdout as newline-delimited JSON. This is the default when `--upstream` is provided, allowing MCP clients to spawn intercept as a command while the upstream is a remote HTTP server.

```sh
intercept -c policy.yaml --upstream https://mcp.stripe.com
intercept -c policy.yaml --upstream https://mcp.stripe.com --header "Authorization: Bearer tok"
```

**HTTP/SSE** (activates with `--transport http` or `--transport sse`):

Intercept runs a local HTTP server and proxies requests to the upstream MCP server URL. On startup, it prints the local endpoint URL to stderr (e.g., `Intercept listening on http://127.0.0.1:4821`). When `--port` is `0`, an available port is selected automatically.

```sh
intercept -c policy.yaml --transport http --upstream https://mcp.example.com
intercept -c policy.yaml --transport sse --upstream https://mcp.example.com/sse --port 8080
```

### Examples

```sh
# Stdio proxy with debug logging
intercept -c policy.yaml --log-level debug -- npx -y @modelcontextprotocol/server-github

# Stdio bridge to remote HTTP server with auth header
intercept -c policy.yaml --upstream https://mcp.stripe.com --header "Authorization: Bearer tok"

# HTTP proxy with explicit port
intercept -c policy.yaml --transport http --upstream https://mcp.example.com --port 9000

# Shared Redis state with key prefix
intercept -c policy.yaml --state-dsn redis://localhost:6379/0 --state-prefix github: -- npx server-github
```

## Scan command

Connects to an MCP server, discovers all available tools, and generates a [policy](POLICY.md) scaffold.

```
intercept scan [flags] -- <upstream-command...>
intercept scan [flags] --upstream <url>
```

### Flags

| Flag | Short | Type | Default | Description |
|---|---|---|---|---|
| `--upstream` | | string | | Upstream server URL |
| `--header` | | string[] | | Custom headers for upstream requests (e.g. `"Authorization: Bearer tok"`) |
| `--output` | `-o` | string | stdout | Write output to file instead of stdout |
| `--timeout` | | duration | `30s` | Max time to wait for server startup and tool listing |

### Output

The generated YAML lists every tool alphabetically with descriptions and parameter summaries as comments, and a commented-out global rate limit wildcard example at the end. Edit the `rules: []` entries to add your [policy rules](POLICY.md), and uncomment the wildcard section for a global rate limit.

### Examples

```sh
# Scan a stdio server, print to stdout
intercept scan -- npx -y @modelcontextprotocol/server-github

# Scan an HTTP server
intercept scan --upstream https://mcp.example.com

# Scan with auth header
intercept scan --upstream https://mcp.stripe.com --header "Authorization: Bearer tok"

# Write to file
intercept scan -o policy.yaml -- npx -y @modelcontextprotocol/server-github
```

## Validate command

Checks a [policy file](POLICY.md) for errors without starting the proxy.

```
intercept validate -c <policy-file>
```

### Flags

| Flag | Short | Type | Default | Description |
|---|---|---|---|---|
| `--config` | `-c` | string | (required) | Path to the policy YAML file |

### Output

Prints `Policy file is valid.` on success, or lists all validation errors. See [POLICY.md, Validation errors](POLICY.md#validation-errors) for the full list.

```sh
$ intercept validate -c policy.yaml
Policy file is valid.
```

## Status command

Shows a summary of Intercept proxy instances and recent activity by reading event logs.

```
intercept status
```

No flags. The command reads event files from `~/.intercept/events/` and prints:

- **Instances**: table of all known proxy instances with ID, server, PID, state backend, fail mode, status (alive/dead/unknown), and last seen time.
- **Stats**: total tool calls, denied count and percentage, calls per minute (over last 5 minutes).
- **Top Deny Rules**: the 10 most frequently triggered deny rules.
- **Recent Denied Calls**: the last 10 denied tool calls with timestamp, tool name, and rule.

Sections with no data are omitted.

## MCP client integration

To use Intercept with Claude Code or any MCP client that reads `.mcp.json`, point the server's `command` at Intercept.

**With a child process (stdio):**

```json
{
  "mcpServers": {
    "github": {
      "command": "intercept",
      "args": [
        "-c", "/path/to/policy.yaml",
        "--",
        "npx", "-y", "@modelcontextprotocol/server-github"
      ],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "..."
      }
    }
  }
}
```

Environment variables defined in `env` are passed through to the upstream server process.

**With a remote HTTP server:**

```json
{
  "mcpServers": {
    "stripe": {
      "command": "intercept",
      "args": [
        "-c", "/path/to/policy.yaml",
        "--upstream", "https://mcp.stripe.com",
        "--header", "Authorization: Bearer tok"
      ]
    }
  }
}
```

Intercept bridges stdio (from the MCP client) to HTTP (to the upstream server). The AI agent interacts with Intercept exactly as it would with the upstream server; Intercept is fully transparent aside from policy enforcement.

## Hot reload

Intercept watches the [policy file](POLICY.md) for changes using filesystem notifications. Edits are debounced (100ms) to handle editors that write files in multiple steps.

On reload:

- New rule definitions take effect immediately for subsequent tool calls.
- In-flight calls (already past policy evaluation) are not affected.
- Stateful counters are **not** reset. Counters persist independently of rule definitions to prevent gaming limits by editing config.
- If the new file is invalid YAML or fails validation, the reload is rejected and the previous config remains active. A warning is logged.
- A log message is emitted on successful reload.

## State backends

Intercept supports two state backends for [stateful counters](POLICY.md#stateful-counters). Only one can be active at a time.

### SQLite (default)

Used when `--state-dsn` is not set. Stores counters in a SQLite database at `<state-dir>/intercept.sqlite` (default: `~/.intercept/state/intercept.sqlite`). Runs in WAL mode for safe multi-process access.

### Redis

Used when `--state-dsn` is set to a Redis URL.

```sh
intercept -c policy.yaml --state-dsn redis://localhost:6379/0 -- npx server
```

DSN format follows standard Redis URLs: `redis://[user:password@]host:port[/db]`.

The `--state-fail-mode` flag controls behavior when Redis is unreachable:

| Mode | Behavior |
|---|---|
| `closed` (default) | Deny tool calls that require state evaluation |
| `open` | Allow tool calls, treating counters as zero |

The `--state-prefix` flag prepends a string to all counter keys, allowing multiple Intercept instances to share a Redis database with isolated namespaces:

```sh
intercept -c policy.yaml --state-dsn redis://localhost:6379/0 --state-prefix stripe-prod: -- npx stripe-server
```

## Event logging

Each Intercept proxy instance appends structured events to `~/.intercept/events/<instance-id>.jsonl` in newline-delimited JSON format.

### Event types

| Type | Description |
|---|---|
| `startup` | Proxy started, includes server name, PID, config path, state backend |
| `tool_call` | Tool call processed, includes tool name, result (`allowed`/`denied`), rule name if denied |
| `config_reload` | Policy file reloaded, includes status (`success`/`failure`) |
| `heartbeat` | Periodic (60s) snapshot of counter values |
| `shutdown` | Proxy shutting down |

Tool call arguments are not logged by default for privacy. Only a SHA-256 hash of the arguments is recorded for correlation.

### Retention

Event files older than 7 days are automatically pruned on proxy startup and when running `intercept status`.

## Paths and directories

| Path | Description |
|---|---|
| `~/.intercept/state/` | Default state directory (contains `intercept.sqlite`) |
| `~/.intercept/events/` | Event log directory (contains `<instance-id>.jsonl` files) |
