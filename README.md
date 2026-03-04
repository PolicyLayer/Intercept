# Intercept

Intercept is a deterministic enforcement proxy for the Model Context Protocol (MCP). It sits between an AI agent and an MCP server, evaluating every `tools/call` request against YAML-defined policies. Violating calls are blocked at the transport layer before reaching the upstream server.

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

## What it does

- **Block tool calls** — deny dangerous tools unconditionally (e.g. `delete_repository`)
- **Validate arguments** — enforce constraints on tool arguments (`amount <= 500`, `currency in [usd, eur]`)
- **Rate limit** — cap calls per minute, hour, or day with `rate_limit: 5/hour` shorthand
- **Track spend** — stateful counters with dynamic increments (e.g. sum `args.amount` across calls)
- **Hide tools** — strip tools from `tools/list` so the agent never sees them, saving context window tokens
- **Default deny** — allowlist mode where only explicitly listed tools are permitted
- **Hot reload** — edit the policy file while running; changes apply immediately without restart
- **Validate policies** — `intercept validate -c policy.yaml` catches errors before deployment

## Install

**npx:**

```sh
npx -y @policylayer/intercept -c policy.yaml --upstream https://mcp.stripe.com --header "Authorization: Bearer sk_live_..."
```

**npm:**

```sh
npm install -g @policylayer/intercept
```

**Go:**

```sh
go install github.com/policylayer/intercept@latest
```

**Pre-built binaries:**

Download from [GitHub Releases](https://github.com/policylayer/intercept/releases) and place the binary on your PATH.

## Quick start

**1. Generate a policy scaffold from a running MCP server:**

```sh
intercept scan -o policy.yaml -- npx -y @modelcontextprotocol/server-stripe
```

This connects to the server, discovers all available tools, and writes a commented YAML file listing each tool with its parameters.

**2. Edit the policy to add rules:**

```yaml
version: "1"
description: "Stripe MCP server policies"

hide:
  - delete_customer
  - delete_product
  - delete_invoice

tools:
  create_charge:
    rules:
      - name: "max single charge"
        conditions:
          - path: "args.amount"
            op: "lte"
            value: 50000
        on_deny: "Single charge cannot exceed $500.00"

      - name: "daily spend cap"
        conditions:
          - path: "state.create_charge.daily_spend"
            op: "lte"
            value: 1000000
        on_deny: "Daily spending cap of $10,000.00 reached"
        state:
          counter: "daily_spend"
          window: "day"
          increment_from: "args.amount"

      - name: "allowed currencies"
        conditions:
          - path: "args.currency"
            op: "in"
            value: ["usd", "eur"]
        on_deny: "Only USD and EUR charges are permitted"

  create_refund:
    rules:
      - name: "refund limit"
        rate_limit: 10/day
        on_deny: "Daily refund limit (10) reached"
```

**3. Run the proxy:**

```sh
intercept -c policy.yaml --upstream https://mcp.stripe.com --header "Authorization: Bearer sk_live_..."
```

Intercept proxies all MCP traffic and enforces your policy on every tool call. Hidden tools are stripped from the agent's view entirely.

## Example policies

The `policies/` directory contains ready-made policy scaffolds for 43 popular MCP servers including GitHub, Stripe, AWS, Notion, Slack, and more. Each file lists every tool with its description, grouped by category (Read, Write, Execute, Financial, Destructive).

Copy one as a starting point:

```sh
cp policies/stripe.yaml policy.yaml
# edit to add your rules, then:
intercept -c policy.yaml --upstream https://mcp.stripe.com
```

Browse all policies → [policies/](policies/)

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

## State backends

Rate limits and counters persist across restarts. SQLite is the default (zero config). Redis is supported for multi-instance deployments:

```sh
intercept -c policy.yaml --state-dsn redis://localhost:6379 --upstream https://mcp.stripe.com
```

## Documentation

- [CLI reference](USAGE.md): all commands, flags, transport modes, state backends, event logging
- [Policy reference](POLICY.md): YAML format, conditions, operators, stateful counters, examples
- [Example policies](policies/): ready-made scaffolds for 43 MCP servers

## License

[Apache 2.0](LICENSE)
