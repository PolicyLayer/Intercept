# Policy Reference

This document covers the YAML policy file format used by Intercept to enforce rules on MCP tool calls. For CLI commands and flags, see [USAGE.md](USAGE.md).

## Overview

A policy file defines which tool calls are allowed, denied, or rate-limited. Intercept loads the policy on startup and evaluates every incoming `tools/call` request against it. Calls that pass all rules are forwarded to the upstream MCP server. Calls that fail any rule receive a denial message instead.

## Top-level structure

```yaml
version: "1"
description: "Human-readable description of this policy"

tools:
  <tool_name>:
    rules:
      - name: "rule name"
        # ...rule definition...
  "*":
    rules:
      - name: "applies to all tools"
        # ...
```

| Field | Required | Description |
|---|---|---|
| `version` | yes | Must be `"1"` |
| `description` | no | Human-readable description of the policy |
| `default` | no | Default posture: `"allow"` (default) or `"deny"` |
| `tools` | yes | Map of tool names to their rule definitions |

Tool name keys must match the exact tool names exposed by the MCP server. The special key `"*"` defines wildcard rules that apply to every tool call.

## Default policy posture

By default, Intercept uses an **allow** posture: any tool call that doesn't match a rule is permitted. Setting `default: deny` flips to an allowlist model where only tools explicitly listed under `tools` are permitted. Unlisted tools are rejected before any rules (including wildcard rules) are evaluated.

```yaml
version: "1"
default: deny

tools:
  read_file:
    rules: []          # allowed with no additional checks

  create_issue:
    rules:
      - name: "hourly limit"
        rate_limit: 5/hour

  # Any tool not listed above is automatically denied.
```

When `default: deny` is active:

- A tool listed under `tools` with `rules: []` is allowed unconditionally.
- A tool listed under `tools` with rules is evaluated normally.
- Wildcard (`"*"`) rules still apply to listed tools, but do not rescue unlisted tools.

## Rules

Each tool entry contains a list of rules. Every rule must have a `name`.

```yaml
tools:
  create_issue:
    rules:
      - name: "hourly issue limit"
        conditions:
          - path: "state.create_issue.hourly_count"
            op: "lte"
            value: 5
        on_deny: "Hourly limit of 5 new issues reached"
        state:
          counter: "hourly_count"
          window: "hour"
```

### Action

The `action` field controls how the rule behaves:

| Action | Behaviour | Conditions allowed? |
|---|---|---|
| `"evaluate"` (default) | Checks conditions; denies the call if any condition fails | Required (at least one) |
| `"deny"` | Unconditionally blocks the tool call | Not allowed |

If `action` is omitted, it defaults to `"evaluate"`.

### on_deny

The `on_deny` field is an optional message returned to the AI agent when a rule denies a tool call. The agent sees it prefixed with `[INTERCEPT POLICY DENIED]`:

```yaml
on_deny: "Hourly limit of 5 new issues reached. Wait before creating more."
```

The agent receives:

```
[INTERCEPT POLICY DENIED] Hourly limit of 5 new issues reached. Wait before creating more.
```

## Rate limit shorthand

For simple "N calls per window" limits, use the `rate_limit` shorthand instead of writing conditions and state blocks manually:

```yaml
tools:
  create_issue:
    rules:
      - name: "hourly issue limit"
        rate_limit: 5/hour
        on_deny: "Hourly limit of 5 new issues reached"
```

The format is `<count>/<window>`, where `count` is a positive integer and `window` is `minute`, `hour`, or `day`.

This expands internally to an `evaluate` rule with a `lte` condition on an auto-generated counter (`_rate_<window>`) and a matching `state` block. If `on_deny` is omitted, a default message is generated: "Rate limit of N per window reached. Try again later."

Restrictions:
- Cannot be combined with `conditions` or `state` (use the full syntax for advanced cases like dynamic increments or custom counters)
- Cannot be used with `action: "deny"`
- Two rules with the same window on the same tool produce a duplicate counter error (use different windows, or combine into a single rule)

For wildcard (`"*"`) tools, the counter is scoped to `_global` as usual.

## Conditions

Conditions compare a value at a given `path` against an expected `value` using an `op` (operator). All conditions within a rule are ANDed: every condition must pass for the rule to allow the call.

```yaml
conditions:
  - path: "args.amount"
    op: "lte"
    value: 50000
```

### Path syntax

Paths use dot-notation to reach into data:

- `args.<field>`: reads from the tool call arguments (e.g., `args.amount`, `args.metadata.key`)
- `state.<scope>.<counter>`: reads a stateful counter value (e.g., `state.create_issue.hourly_count`)

For nested arguments, each dot descends one level. Given tool arguments `{"metadata": {"key": "val"}}`, the path `args.metadata.key` resolves to `"val"`.

### Operators

| Operator | Value type | Description | Example |
|---|---|---|---|
| `eq` | any | Equal (numeric or string) | `op: "eq"`, `value: "main"` |
| `neq` | any | Not equal | `op: "neq"`, `value: "draft"` |
| `in` | list | Value is in list | `op: "in"`, `value: ["usd", "eur"]` |
| `not_in` | list | Value is not in list | `op: "not_in"`, `value: ["admin"]` |
| `lt` | numeric | Less than | `op: "lt"`, `value: 100` |
| `lte` | numeric | Less than or equal | `op: "lte"`, `value: 50000` |
| `gt` | numeric | Greater than | `op: "gt"`, `value: 0` |
| `gte` | numeric | Greater than or equal | `op: "gte"`, `value: 1` |
| `regex` | string | Matches regular expression | `op: "regex"`, `value: "^feat/"` |
| `contains` | any | Substring match (strings) or element match (lists) | `op: "contains"`, `value: "test"` |
| `exists` | boolean | Field is present (`true`) or absent (`false`) | `op: "exists"`, `value: true` |

### AND logic

All conditions within a single rule are ANDed. Both must pass:

```yaml
- name: "safe charge"
  conditions:
    - path: "args.amount"
      op: "lte"
      value: 50000
    - path: "args.currency"
      op: "in"
      value: ["usd", "eur"]
```

Across rules, all rules for a tool must also pass. If any single rule denies the call, the call is denied.

## Stateful counters

Rules can track call counts or accumulated values across a time window using a `state` block:

```yaml
- name: "daily spend cap"
  conditions:
    - path: "state.create_charge.daily_spend"
      op: "lt"
      value: 1000000
  on_deny: "Daily spending cap of $10,000.00 reached"
  state:
    counter: "daily_spend"
    window: "day"
    increment_from: "args.amount"
```

### Fields

| Field | Required | Default | Description |
|---|---|---|---|
| `counter` | yes | | Name of the counter. Referenced in conditions as `state.<tool>.<counter>`. |
| `window` | yes | | Time window: `"minute"`, `"hour"`, or `"day"`. |
| `increment` | no | `1` | Fixed amount to add on each allowed call. |
| `increment_from` | no | | Path to a tool argument (e.g., `args.amount`) whose value is used as the increment instead of the fixed `increment`. |

### Windows

Windows are calendar-aligned in UTC:

| Window | Resets at |
|---|---|
| `"minute"` | The start of each UTC minute (e.g., 10:31:00.000Z) |
| `"hour"` | The top of each UTC hour (e.g., 11:00:00.000Z) |
| `"day"` | Midnight UTC (00:00:00.000Z) |

A counter's value resets to zero when a new window begins. For example, a `"day"` counter that reached 47 at 23:59 UTC will read as 0 at 00:00 UTC.

### Reserve and rollback

When a tool call is evaluated, Intercept uses a two-phase model:

1. **Reserve**: the counter is atomically read and tentatively incremented. If the post-increment value would exceed the limit, the call is denied immediately without changing the counter.
2. **Forward**: the call is sent to the upstream MCP server.
3. **Commit or rollback**: if the upstream call succeeds, the reservation stands. If the upstream call fails, the increment is rolled back so a failed call does not consume quota.

### Counter scoping

For tool-specific rules, counters are scoped as `state.<tool_name>.<counter>`:

```yaml
tools:
  create_issue:
    rules:
      - name: "hourly limit"
        conditions:
          - path: "state.create_issue.hourly_count"
            op: "lte"
            value: 5
        state:
          counter: "hourly_count"
          window: "hour"
```

For wildcard (`"*"`) rules, counters use the `_global` scope:

```yaml
"*":
  rules:
    - name: "global rate limit"
      conditions:
        - path: "state._global.calls_per_minute"
          op: "lte"
          value: 60
      state:
        counter: "calls_per_minute"
        window: "minute"
```

### Dynamic increments

Use `increment_from` to increment a counter by the value of a tool argument instead of a fixed amount. This is useful for tracking accumulated totals like spending:

```yaml
- name: "daily spend cap"
  conditions:
    - path: "state.create_charge.daily_spend"
      op: "lt"
      value: 1000000
  on_deny: "Daily spending cap of $10,000.00 reached"
  state:
    counter: "daily_spend"
    window: "day"
    increment_from: "args.amount"
```

Each allowed `create_charge` call increments the `daily_spend` counter by whatever `args.amount` is (e.g., 5000 for a $50.00 charge in cents).

## Wildcard rules

The special tool name `"*"` defines rules that apply to every tool call, in addition to any tool-specific rules. Wildcard rules are evaluated after tool-specific rules.

State counters under wildcard rules use the `_global` scope, so the condition path is `state._global.<counter>`.

## Rule evaluation order

When a `tools/call` request arrives, Intercept processes it as follows:

0. If `default: deny` is set and the tool is **not** listed under `tools`, deny immediately. Wildcard rules do not apply to unlisted tools.
1. Look up rules for the specific tool name.
2. Look up wildcard (`"*"`) rules.
3. Evaluate all matching rules in order (tool-specific first, then wildcard):
   - If a rule has `action: "deny"`, the call is denied immediately.
   - If a rule has `action: "evaluate"`, all its conditions must pass.
4. If any rule denies the call, the `on_deny` message from that rule is returned to the agent.
5. If all rules pass, reserve any stateful counters atomically.
6. Forward the call to the upstream server.
7. On success, commit the counter increments. On failure, roll back.

If `default` is `"allow"` (or omitted) and no rules match a tool, the call is allowed.

## Complete examples

### GitHub MCP server

Rate-limits issue and PR creation, blocks repository deletion, and enforces a global rate limit across all tools.

```yaml
version: "1"
description: "GitHub MCP server policies"

tools:
  create_issue:
    rules:
      - name: "hourly issue limit"
        rate_limit: 5/hour
        on_deny: "Hourly limit of 5 new issues reached"

  create_pull_request:
    rules:
      - name: "hourly pr limit"
        rate_limit: 3/hour
        on_deny: "Hourly limit of 3 new pull requests reached"

  delete_repository:
    rules:
      - name: "block repo deletion"
        action: "deny"
        on_deny: "Repository deletion is not permitted via AI agents"

  "*":
    rules:
      - name: "global rate limit"
        rate_limit: 60/minute
```

### Stripe MCP server

Caps individual charge amounts, tracks daily spending with dynamic increments, restricts currencies, limits refunds, and blocks customer deletion.

```yaml
version: "1"
description: "Stripe MCP server policies"

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
        conditions:
          - path: "args.amount"
            op: "lte"
            value: 10000
        on_deny: "Refunds over $100.00 require manual processing"

      - name: "daily refund count"
        conditions:
          - path: "state.create_refund.daily_count"
            op: "lte"
            value: 10
        on_deny: "Daily refund limit (10) reached"
        state:
          counter: "daily_count"
          window: "day"

  delete_customer:
    rules:
      - name: "block destructive action"
        action: "deny"
        on_deny: "Customer deletion is not permitted via AI agents"

  "*":
    rules:
      - name: "global rate limit"
        conditions:
          - path: "state._global.calls_per_minute"
            op: "lte"
            value: 60
        on_deny: "Rate limit: maximum 60 tool calls per minute"
        state:
          counter: "calls_per_minute"
          window: "minute"
```

## Validation errors

Run `intercept validate -c policy.yaml` to check a policy file for errors. Every possible error is listed below with its cause and fix.

| Error | Cause | Fix |
|---|---|---|
| `version must be "1", got "<x>"` | The `version` field is missing or not `"1"` | Set `version: "1"` |
| `default must be "allow" or "deny", got "<x>"` | The `default` field has an unrecognised value | Use `"allow"` or `"deny"`, or omit the field |
| `rule must have a name` | A rule is missing the `name` field | Add a `name` to the rule |
| `action must be "evaluate" or "deny", got "<x>"` | Unrecognised action value | Use `"evaluate"` or `"deny"` |
| `deny rules must not have conditions` | A rule with `action: "deny"` has a `conditions` list | Remove `conditions` from deny rules |
| `evaluate rules must have at least one condition` | A rule with `action: "evaluate"` has no conditions | Add at least one condition |
| `path must start with "args." or "state.", got "<x>"` | Condition path does not reference tool arguments or state | Use `args.<field>` or `state.<scope>.<counter>` |
| `unknown operator "<op>"` | The `op` field contains an unrecognised operator | Use one of: `eq`, `neq`, `in`, `not_in`, `lt`, `lte`, `gt`, `gte`, `regex`, `contains`, `exists` |
| `operator "<op>" requires a list value` | `in` or `not_in` was given a non-list value | Provide a YAML list (e.g., `["a", "b"]`) |
| `operator "<op>" requires a numeric value` | `lt`, `lte`, `gt`, or `gte` was given a non-numeric value | Provide a number |
| `operator "exists" requires a boolean value` | `exists` was given a non-boolean value | Use `true` or `false` |
| `regex value must be a string` | The `regex` operator was given a non-string value | Provide a string pattern |
| `invalid regex "<pattern>": <error>` | The regex pattern does not compile | Fix the regular expression syntax |
| `counter must not be empty` | The `state.counter` field is blank | Provide a counter name |
| `window must be "minute", "hour", or "day", got "<x>"` | Unrecognised window value | Use `"minute"`, `"hour"`, or `"day"` |
| `increment_from must start with "args.", got "<x>"` | Dynamic increment does not reference a tool argument | Use a path like `args.amount` |
| `condition references state.<tool>.<counter> but no matching state block found` | A condition reads a counter that no rule defines with a `state` block | Add a `state` block with the matching `counter` name to a rule for that tool |
| `rate_limit count must be a positive integer, got "<x>"` | The count in `rate_limit` is not a positive number | Use a positive integer (e.g., `5/hour`) |
| `rate_limit window must be "minute", "hour", or "day", got "<x>"` | Unrecognised window in `rate_limit` | Use `minute`, `hour`, or `day` |
| `rate_limit cannot be combined with conditions or state` | A rule uses `rate_limit` alongside `conditions` or `state` | Use separate rules, or switch to the full syntax |
| `rate_limit cannot be used with action "deny"` | A deny rule also has `rate_limit` | Remove `rate_limit` or change the action |
| `duplicate state counter "<name>" (also used by rules[N])` | Two rules on the same tool define the same counter name | Use different counter names or different windows |
