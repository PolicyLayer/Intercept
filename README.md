# Intercept

Intercept is a transparent proxy that sits between an AI agent and an MCP (Model Context Protocol) server. It intercepts every tool call the agent makes and enforces policy rules defined in a YAML file: argument validation, rate limiting, unconditional blocks, and more. If a call violates policy, Intercept returns a denial message to the agent instead of forwarding the call.

```
┌──────────┐       ┌───────────┐       ┌────────────┐
│ LLM/AI   │──────>│ Intercept │──────>│ MCP Server │
│ Client   │<──────│  (proxy)  │<──────│ (upstream) │
└──────────┘       └───────────┘       └────────────┘
                        │
                   ┌────┴────┐
                   │ Policy  │
                   │ Engine  │
                   └────┬────┘
                   ┌────┴────┐
                   │ State   │
                   │ Store   │
                   └─────────┘
```

## Install

```sh
go install github.com/policylayer/intercept@latest
```

## Quick start

**1. Generate a policy scaffold from a running MCP server:**

```sh
intercept scan -o policy.yaml -- npx -y @modelcontextprotocol/server-github
```

This connects to the server, discovers all available tools, and writes a commented YAML file listing each tool with its parameters.

**2. Edit the policy to add rules.** For example, block repository deletion:

```yaml
delete_repository:
  rules:
    - name: "block repo deletion"
      action: "deny"
      on_deny: "Repository deletion is not permitted"
```

**3. Run the proxy:**

```sh
intercept -c policy.yaml -- npx -y @modelcontextprotocol/server-github
```

Intercept launches the upstream server as a subprocess, proxies all MCP traffic, and enforces your policy on every tool call.

## MCP client integration

To use Intercept with Claude Code (or any MCP client that reads `.mcp.json`), point the server command at Intercept:

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

For remote HTTP servers, use `--upstream` instead of a command:

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

## Documentation

- [CLI reference](USAGE.md): all commands, flags, transport modes, state backends, event logging
- [Policy reference](POLICY.md): YAML format, conditions, operators, stateful counters, examples

## License

[MIT](LICENSE)
