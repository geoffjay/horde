# Agent Adapter Protocol (AAP) — v1

Status: **Draft**
Protocol version: **1**
Canonical home: **horde** (`github.com/geoffjay/horde`)

## 1. Purpose and scope

The Agent Adapter Protocol (AAP) is a vendor-neutral wire protocol for communication between a **host**
(an orchestrator such as horde or agentd) and an AI coding agent (the **agent**). It defines how a host
launches an agent, exchanges prompts and responses, streams incremental output, negotiates tool-use
approval, scopes file-system permissions, and reports usage.

AAP is deliberately **not** tied to any single vendor or host. An agent developer implements AAP in an
**adapter** — a process that translates AAP to and from their agent's native format. A reference adapter
for Claude Code exists (`agentd-adapter-claude`), but Claude Code holds no privileged position: it is one
adapter among many, and the same adapter runs unchanged under any conforming host.

> **History.** AAP is the generalization of the protocol that originated in agentd as the "agentd Agent
> Protocol." The acronym is preserved; the name is now host-neutral, and horde is the canonical home of
> the specification going forward. The v1 **message schema is compatible with agentd's original v1** —
> same `type` tags and field semantics — and evolves **additively** (see §11). Any AAP v1 adapter built
> for agentd interoperates with horde and vice versa.

### 1.1 What AAP governs — and what it does not

AAP governs the **local, one-host-to-one-adapter programmatic path** only: a single host process driving
a single agent process over one connection. It does **not** govern:

- **Where** the agent process runs. That is the concern of the host's execution backend (subprocess,
  tmux, pty). AAP rides on top of whichever backend is active.
- **Interactive PTY mode**, in which a human types directly into a terminal running an agent. That path
  bypasses AAP entirely.
- **Multi-user, cross-node, or remote-principal concerns.** In a clustered host (e.g. horde's
  master/slave topology), *who* may address an agent, whether a remote principal may send a *mutating*
  prompt vs. a read-only query, directory synchronization, and default-restrictive remote permissions
  all live in the **host's node-authorization and cluster layer, above AAP**. AAP is the local
  enforcement point at the tool/write boundary (via tool approval, §6.4, and permission scope, §6.6);
  it carries no notion of remote principals. Keeping that boundary is what keeps AAP vendor-neutral.

## 2. Roles and terminology

| Term | Meaning |
| --- | --- |
| Host | The orchestrator. Owns policy, persistence, authorization, and streaming to end users. |
| Agent | An AI coding agent, driven through an adapter. |
| Adapter | A process implementing AAP that wraps a specific agent. |
| Turn | One prompt and the agent's complete response to it, identified by a `turn_id`. |
| Tool call | A request by the agent to invoke a tool, identified by a `call_id`. |
| Binding | A concrete byte transport carrying AAP frames (§4). |

Direction notation: **H→A** is host-to-agent (adapter stdin / host-sent), **A→H** is agent-to-host
(adapter stdout / host-received).

## 3. Framing

- The wire format is **newline-delimited JSON (NDJSON)**: exactly one JSON object per line, terminated
  by a single `\n` (U+000A). Objects MUST NOT contain unescaped newlines.
- Encoding is **UTF-8**.
- Every message is a JSON object with a top-level string field **`type`** that discriminates the
  message.
- Receivers **MUST ignore unknown fields** on a known message type (forward compatibility).
- Receivers **MUST NOT** treat an unknown `type` as fatal: log it and skip the line.
- Empty and whitespace-only lines MUST be ignored.

## 4. Transports (bindings)

AAP is bidirectional: after a turn begins, the host may send `cancel` and `approval_response` while the
agent is still emitting output, and the agent may send `approval_request` mid-turn. **Every binding MUST
therefore be full-duplex.** The message schema is identical across bindings; only the byte transport
differs.

An adapter MUST support the stdio binding and MAY support the websocket binding. The host selects the
binding at launch and communicates the choice through environment variables (§5).

### 4.1 stdio (mandatory baseline)

- Host→agent AAP frames are written to the adapter's **stdin**.
- Agent→host AAP frames are written to the adapter's **stdout**.
- The adapter's **stderr** is reserved for human-readable logs and MUST NOT carry AAP frames.
- Full-duplex (stdin and stdout are independent).
- Environment: `AAP_TRANSPORT=stdio`.

### 4.2 websocket (optional)

- The adapter dials back to a host-provided WebSocket URL and exchanges the same AAP frames as text
  messages (one JSON object per WebSocket text frame; the trailing `\n` is optional over WebSocket).
- Full-duplex.
- Environment: `AAP_TRANSPORT=websocket`, `AAP_WS_URL=ws://host:port/path`.

### 4.3 Non-bindings

**HTTP + Server-Sent Events (SSE) is not an AAP binding.** SSE is one-directional (server→client) and
cannot carry host→agent control frames mid-turn. Where a host uses HTTP+SSE for its own external API or
for a non-interactive, single-shot invocation path, that is a separate transport outside AAP and MUST
NOT be described as an AAP binding.

## 5. Invocation contract

1. The host resolves an **adapter command** (argv + environment) from the agent's configured type.
2. The host launches that command via the active execution backend, injecting the AAP transport
   environment variables (§4).
3. AAP prescribes **no** command-line flags for the underlying agent. The adapter owns native argv
   construction.
4. All per-agent configuration is delivered in the `initialize` message (§6.1), **not** via argv.
   Configuration flows as data over the protocol, and the adapter maps it to native invocation.

### 5.1 Environment variables

| Canonical | Legacy alias (deprecated) | Meaning |
| --- | --- | --- |
| `AAP_TRANSPORT` | `AGENTD_AAP_TRANSPORT` | Binding selector: `stdio` or `websocket`. |
| `AAP_WS_URL` | `AGENTD_AAP_WS_URL` | WebSocket URL for the `websocket` binding. |

Hosts SHOULD set the canonical `AAP_*` variables. For interoperability with agents originally written
against agentd, hosts MAY additionally set the legacy `AGENTD_AAP_*` variables, and adapters MUST accept
the legacy names as aliases when the canonical names are absent (canonical takes precedence when both are
present). The legacy names are deprecated and may be removed in a future major version.

## 6. Message reference

### 6.1 Handshake

#### `initialize` (H→A)

Sent once, first, before any other host message. Carries all agent configuration.

```json
{
  "type": "initialize",
  "protocol_version": 1,
  "model": "claude-sonnet-5",
  "system_prompt": { "mode": "replace", "text": "You are ...", "path": null },
  "workspace": { "cwd": "/repo", "additional_dirs": ["/other"], "worktree": false },
  "tools": {
    "mcp_servers": {
      "horde": { "command": "horde", "args": ["mcp"], "env": { "HORDE_HORDE_SERVER_URL": "http://..." } }
    }
  },
  "permissions": { "mode": "read_only", "writable_paths": ["docs/"], "deny_paths": [".git/", ".env"] },
  "resume_token": null
}
```

- `protocol_version` (integer, required): the AAP version the host speaks. The adapter MUST refuse
  (emit a fatal `error`) if it cannot speak this version.
- `model` (string, optional): requested model. If omitted, the adapter uses its default.
- `system_prompt` (object, optional): `mode` is `"replace"` or `"append"`. Exactly one of `text` or
  `path` is set.
- `workspace.cwd` (string, required): working directory.
- `workspace.additional_dirs` (array of string, optional): extra directories the agent may access.
- `workspace.worktree` (boolean, optional): run in an isolated worktree if supported.
- `tools.mcp_servers` (object, optional): MCP server definitions, keyed by name. Each is
  `{ command, args, env }` (capability `mcp`).
- `permissions` (object, optional): up-front file-system permission scope the adapter self-enforces
  (capability `permissions`, §6.6).
- `resume_token` (string, optional): an opaque token from a prior `turn_complete` used to resume a
  conversation (capability `resume`).

#### `ready` (A→H)

Sent once the adapter has started its native agent and is prepared to accept prompts.

```json
{
  "type": "ready",
  "protocol_version": 1,
  "agent": { "name": "claude-code", "version": "2.1.x" },
  "capabilities": ["streaming", "thinking", "tool_approval", "usage_reporting",
                   "cost_reporting", "context_clear", "cancel", "mcp",
                   "system_prompt_append", "permissions"],
  "models": ["claude-sonnet-5", "claude-opus-4-8"]
}
```

- `capabilities` (array of string, required): the capability tokens the adapter supports (§7).
- `models` (array of string, optional): models the adapter can serve.

The host **MUST NOT** send a `prompt` before receiving `ready`.

### 6.2 Turn input (H→A)

#### `prompt`

```json
{ "type": "prompt", "turn_id": "t1", "content": "Refactor the parser." }
```

- `content` may be a string or an array of content blocks (`[{"type":"text","text":"..."}]`).
- The host assigns a unique `turn_id`; the adapter echoes it on all output for that turn.

#### `cancel` (capability `cancel`)

```json
{ "type": "cancel", "turn_id": "t1" }
```

Requests interruption of the active turn. If `turn_id` is omitted, cancels the current turn.

#### `clear_context` (capability `context_clear`)

```json
{ "type": "clear_context" }
```

Discards conversation history and starts a fresh context.

#### `shutdown`

```json
{ "type": "shutdown" }
```

Requests graceful termination. The adapter tears down its native agent, flushes output, and exits.

### 6.3 Turn output (A→H)

#### `message`

```json
{
  "type": "message",
  "turn_id": "t1",
  "content": [
    { "type": "thinking", "text": "The parser is in src/parse.rs ..." },
    { "type": "text", "text": "I'll start by extracting the tokenizer." }
  ]
}
```

Assistant output blocks. `text` blocks are visible output; `thinking` blocks are reasoning
(capability `thinking`). Adapters MAY stream multiple `message` frames per turn (capability `streaming`).

#### `tool_call`

```json
{ "type": "tool_call", "turn_id": "t1", "call_id": "c1", "name": "Bash", "input": { "command": "ls" } }
```

Announces a tool invocation. `call_id` is unique within the turn and correlates with an
`approval_request` (if any).

#### `turn_complete`

```json
{
  "type": "turn_complete",
  "turn_id": "t1",
  "is_error": false,
  "stop_reason": "end_turn",
  "result_text": "Done.",
  "resume_token": null,
  "usage": {
    "input_tokens": 1200, "output_tokens": 340,
    "cache_read_input_tokens": 800, "cache_creation_input_tokens": 0,
    "total_cost_usd": 0.0123, "num_turns": 3,
    "duration_ms": 5000, "duration_api_ms": 4200
  }
}
```

Marks the end of a turn. `usage` is present only with the `usage_reporting` capability
(`total_cost_usd` only with `cost_reporting`). `resume_token` is present only with the `resume`
capability.

#### `status`

```json
{ "type": "status", "state": "busy" }
```

Activity transitions. `state` is `"busy"` or `"idle"`.

#### `log`

```json
{ "type": "log", "level": "info", "message": "spawned claude pid=1234" }
```

Structured diagnostics. `level` is `"info"`, `"warn"`, or `"error"`.

### 6.4 Tool approval

Approval is correlated by `request_id`.

#### `approval_request` (A→H, capability `tool_approval`)

```json
{ "type": "approval_request", "request_id": "r1", "call_id": "c1",
  "tool_name": "Bash", "input": { "command": "rm -rf build" } }
```

#### `approval_response` (H→A)

```json
{ "type": "approval_response", "request_id": "r1", "decision": "allow",
  "updated_input": { "command": "rm -rf build" }, "message": null }
```

- `decision` is `"allow"` or `"deny"`.
- `updated_input` (object, optional): an **opaque passthrough**. When present, the adapter uses it in
  place of the original tool input. When absent, the original input stands.
- `message` (string, optional): a human-readable reason, typically included on denial.

The **host** is the sole authority on approval decisions (via its tool policy). The adapter only asks
and applies the answer. In a clustered host this is the enforcement point at which a remote principal's
restrictions are applied: the host decides `allow`/`deny` per its authorization of the requesting
principal.

### 6.5 Errors

#### `error` (A→H)

```json
{ "type": "error", "fatal": true, "code": "spawn_failed", "message": "claude not found on PATH" }
```

`fatal: true` indicates the adapter is exiting. Non-fatal errors are informational.

### 6.6 Permission scope (capability `permissions`)

The optional `permissions` object in `initialize` gives the host a way to constrain an agent's
file-system access **up front**, independent of the `tool_approval` round-trip. This matters for
restrictive-by-default handling of agents the host does not fully trust (e.g. agents driven on behalf of
a remote principal): a compliant adapter self-enforces the scope even if it never emits
`approval_request`.

```json
{ "mode": "read_only", "writable_paths": ["docs/"], "deny_paths": [".git/", ".env"] }
```

- `mode` (string, required within `permissions`): `"read_only"` or `"read_write"`.
- `writable_paths` (array of string, optional): when `mode` is `"read_write"`, paths (relative to
  `workspace.cwd` unless absolute) the agent may write. Empty or omitted with `read_write` means the
  whole workspace is writable.
- `deny_paths` (array of string, optional): paths the agent MUST NOT read or write, overriding the
  above.

A compliant adapter (advertising `permissions`) MUST refuse writes that violate the scope regardless of
approval. `permissions` and `tool_approval` are complementary defense-in-depth: `permissions` is a static
scope the adapter enforces locally; `tool_approval` is the host's dynamic per-call authority. A host with
strict requirements SHOULD both send a `permissions` scope and act as approval authority, and MAY refuse
to launch an agent that advertises neither for a restricted principal.

## 7. Capabilities and graceful degradation

Adapters advertise capability tokens in `ready.capabilities`. The host adapts its behavior when a
capability is absent:

| Capability | Meaning | Host behavior when absent |
| --- | --- | --- |
| `streaming` | Incremental `message` frames during a turn | Host renders only whole messages |
| `thinking` | Emits `thinking` blocks | No reasoning shown |
| `tool_approval` | Supports `approval_request`/`approval_response` | Per-call approval cannot be honored; policy must resolve to allow/deny before launch; holds disabled |
| `usage_reporting` | Populates `usage` token counts | Usage stats absent (defaults) |
| `cost_reporting` | Populates `usage.total_cost_usd` | Cost absent |
| `context_clear` | Handles `clear_context` | Auto-clear triggers an agent **restart** instead |
| `cancel` | Handles `cancel` | Cancel is best-effort; host may fall back to kill/restart |
| `mcp` | Honors `tools.mcp_servers` | MCP servers not provisioned |
| `system_prompt_append` | Supports `system_prompt.mode = "append"` | Host must send `replace` |
| `resume` | Emits/consumes `resume_token` | Resume unavailable |
| `permissions` | Self-enforces `initialize.permissions` scope | Host relies on node policy + `tool_approval` only; may refuse a restricted principal |

Unknown capability tokens MUST be ignored by the host.

## 8. Lifecycle

```
host launches adapter (transport env set)
        │
        ▼
H→A  initialize
        │
A→H  ready                 (host validates protocol_version + capabilities)
        │
   ┌────┴─────────── per turn ───────────────┐
H→A  prompt(turn_id)                          │
A→H  status(busy)                             │
A→H  message* / tool_call*                    │
      (per tool_call, if approval required:)  │
A→H    approval_request(request_id) ──────────┤
H→A    approval_response(request_id)          │
A→H  turn_complete(turn_id, usage?)           │
A→H  status(idle)                             │
   └──────────────────────────────────────────┘
        │
H→A  shutdown            (or clear_context to reset and continue)
        │
        ▼
adapter exits
```

## 9. Conformance checklist

An adapter is AAP v1 compliant if it:

1. Supports the stdio binding (§4.1) and honors `AAP_TRANSPORT` (accepting the legacy alias, §5.1).
2. Accepts `initialize`, refuses unsupported `protocol_version` with a fatal `error`.
3. Emits exactly one `ready` with an accurate `capabilities` list before accepting prompts.
4. Never emits output for a turn before receiving that turn's `prompt`.
5. Echoes the correct `turn_id` on all turn output and closes each turn with `turn_complete`.
6. If it advertises `tool_approval`, emits `approval_request` for gated calls and honors
   `approval_response` (including `updated_input` passthrough) before proceeding.
7. If it advertises `permissions`, self-enforces the `initialize.permissions` scope (§6.6).
8. Ignores unknown fields and unknown message types without failing.
9. Handles `shutdown` by terminating its native agent and exiting cleanly.
10. Writes only human-readable logs to stderr (stdio binding).

## 10. Conformance kit

A **mock adapter** and a shared **protocol test-vector file** ship with horde for validating both
adapters and hosts:

- **Test vectors:** `internal/aap/testdata/vectors.json` — canonical `{name, direction, message}` cases
  covering every message type. Both horde and agentd validate their (de)serializers against this file;
  it is the shared source of truth for the wire encoding.
- **Mock adapter:** the `internal/aap` package provides `RunMockAdapter`, exposed as the hidden
  `horde aap-mock` subcommand. It speaks the stdio binding, completes the handshake, and answers each
  `prompt` with a deterministic `message` + `turn_complete`. It is a host-side conformance fixture and a
  worked reference for adapter authors.

## 11. Compatibility and versioning

- The v1 message schema is a **compatible superset** of agentd's original v1: every `type` tag and field
  agentd defines has identical meaning here. Fields added by this specification (`initialize.permissions`
  and its `permissions` capability) are **optional and additive**; a receiver that does not know them
  ignores them per §3, so an agentd host and a horde host interoperate with the same adapter.
- Within v1, evolution is additive only: new optional fields, new message types, new capability tokens.
  Removing or repurposing a field, or changing a `type` tag's meaning, requires a new
  `protocol_version`.
- Hosts and adapters negotiate `protocol_version` in `initialize`/`ready`; an adapter that cannot serve
  the host's version emits a fatal `error` (§9, item 2).
