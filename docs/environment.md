# Environment

This document describes all environment data for the horde project: ports,
configuration, environment variables, and services. It should be kept up to
date as the project evolves.

## Ports

| Port  | Service        | Direction       | Notes                                        |
|-------|----------------|-----------------|----------------------------------------------|
| 13420 | horde node API | inbound (HTTP)  | Default node API port (`server.port`).       |
| 13500 | horde test API | inbound (HTTP)  | Used in test fixtures (`testdata/valid.*`).  |

The node API listener ships in Phase 2 (`internal/api`, chi over `net/http`).
`horde serve` binds `server.port`; the TUI connects to it as a client.

## Configuration

horde uses a layered configuration system (vendored from
`github.com/geoffjay/plantd/core/config`, adapted to the `HORDE_` prefix).
Config is loaded in this order, later layers override earlier ones:

1. **Defaults** baked into `internal/config/horde.go`.
2. **Config file** (`horde.yaml`, `horde.json`, or `horde.toml`) searched in:
   - `./` (current directory)
   - `~/.config/horde/`
   - `/etc/horde/`
3. **Environment variables** prefixed `HORDE_*` (dots become
   underscores), e.g. `HORDE_SERVER_PORT=14000`.

An explicit config file path can be set via the `HORDE_CONFIG`
environment variable (any extension: `yaml`, `yml`, `json`, `toml`).

### Configuration keys

| Key                              | Default             | Env var                                | Description                              |
|----------------------------------|---------------------|----------------------------------------|------------------------------------------|
| `env`                            | `development`       | `HORDE_ENV`                            | Environment name.                        |
| `mode`                           | `master`            | `HORDE_MODE`                           | Node role: `master` or `slave`.          |
| `server.port`                    | `13420`             | `HORDE_SERVER_PORT`                    | Node API listen port.                    |
| `server.agent_command`           | *(current binary)*  | `HORDE_SERVER_AGENT_COMMAND`           | Binary used to host agent subprocesses. |
| `server.leader`                  | *(empty)*           | `HORDE_SERVER_LEADER`                  | Master address for a slave to connect to.|
| `server.read_timeout`            | `30`                | `HORDE_SERVER_READ_TIMEOUT`            | API read timeout (seconds).             |
| `server.write_timeout`           | `30`                | `HORDE_SERVER_WRITE_TIMEOUT`           | API write timeout (seconds).            |
| `server.idle_timeout`            | `120`               | `HORDE_SERVER_IDLE_TIMEOUT`            | API idle timeout (seconds).             |
| `cluster.node_id`                | *(empty)*           | `HORDE_CLUSTER_NODE_ID`                | Unique node id within the cluster.       |
| `cluster.discovery_mechanism`    | `static`            | `HORDE_CLUSTER_DISCOVERY_MECHANISM`    | How nodes find each other (`static`).   |
| `agent.socket_dir`               | `/tmp`              | `HORDE_AGENT_SOCKET_DIR`               | Directory for agent unix socket files.  |
| `agent.ready_timeout`            | `5`                 | `HORDE_AGENT_READY_TIMEOUT`            | Seconds to wait for agent ready handshake. |
| `agent.health_poll_interval`     | `30`                | `HORDE_AGENT_HEALTH_POLL_INTERVAL`     | Seconds between agent health polls.     |
| `agent.context_retention`        | `300`               | `HORDE_AGENT_CONTEXT_RETENTION`        | Seconds to retain an agent's context after exit. |
| `agent.context_share`            | `restricted`        | `HORDE_AGENT_CONTEXT_SHARE`             | What a remote (non-loopback) principal sees on this node's own context endpoints: `restricted` (redacted subset + error/approval counts) or `full`. The cross-node master summary is always redacted. |
| `project.workspace_dir`          | `.`                 | `HORDE_PROJECT_WORKSPACE_DIR`           | Default workspace dir for a project whose create request omits `workspace`. |
| `project.context_retention`      | `0`                 | `HORDE_PROJECT_CONTEXT_RETENTION`       | Seconds to retain a finished project's agent contexts before eviction. `0` inherits `agent.context_retention`. |
| `log.formatter`                  | `text`              | `HORDE_LOG_FORMATTER`                  | Log formatter: `text` or `json`.        |
| `log.level`                      | `info`              | `HORDE_LOG_LEVEL`                      | Log level.                               |
| `service.id`                     | `org.horde.Horde`   | `HORDE_SERVICE_ID`                      | Service identifier.                      |

### AAP agent declarations (`agents.<name>.*`)

An external coding agent driven through an [AAP](knowledgebase/concepts/agent-adapter-protocol.md)
adapter is declared under the `agents` map, keyed by agent name. Native ADK
agents (greeter, repeater) are registry-built and need no entry. These keys are
config-file/`HORDE_*` settable but are typically set in a config file (see
[`docs/examples/pi-agent.yaml`](examples/pi-agent.yaml)).

| Key | Default | Description |
|-----|---------|-------------|
| `agents.<name>.kind` | `adk` | Agent kind: `adk` (registry-built native) or `aap` (external adapter). |
| `agents.<name>.command` | *(empty)* | AAP only: the adapter command (argv[0]). |
| `agents.<name>.args` | `[]` | AAP only: adapter argv after the command. |
| `agents.<name>.env` | `[]` | AAP only: extra environment (list of `{key, value}`) merged over the node's environment. The host always forces `AAP_TRANSPORT=stdio`. |
| `agents.<name>.model` | *(empty)* | AAP only: requested model, sent as `initialize.model`. Empty uses the adapter default. |
| `agents.<name>.system_prompt` | *(empty)* | AAP only: system prompt text/path for `initialize.system_prompt`. |
| `agents.<name>.system_prompt_mode` | `replace` | AAP only: `replace` or `append` (append requires the `system_prompt_append` capability). |
| `agents.<name>.permissions.mode` | *(empty)* | AAP only: advisory scope mode `read_only` / `read_write`. Omitting the `permissions` block omits the scope. |
| `agents.<name>.permissions.writable_paths` | `[]` | AAP only: writable paths when mode is `read_write`. |
| `agents.<name>.permissions.deny_paths` | `[]` | AAP only: paths the adapter must not read or write. |
| `agents.<name>.auto_approve` | `false` | AAP only: auto-allow every `approval_request` when the adapter advertises `tool_approval`. |

The pi provider key the adapter needs (e.g. `ANTHROPIC_API_KEY`) is inherited
from the node's own environment — the host passes its environment plus the
configured `env` entries to the adapter subprocess.

### Data and state directories (XDG)

horde persists data to XDG-compliant directories (see the [persistence
decision](knowledgebase/decisions/persistence-and-knowledgebase.md)).

| Env var | Default | Description |
|---------|---------|-------------|
| `HORDE_PATHS_CONFIG_DIR` | `~/.config/horde` | Configuration directory (`horde.yaml`, global project defaults). |
| `HORDE_PATHS_DATA_DIR` | `~/.local/share/horde` | General storage: logs, auth, session data, database files. |
| `HORDE_PATHS_STATE_DIR` | `~/.local/state/horde` | Trivial state: JSON KV, execution state, agent info, prompt history, lock files. |

Per-project configuration lives in `.horde/` within a project's workspace
directory and overrides global config. Every project has a knowledgebase at
`.horde/knowledgebase/` (OKF v0.1).

## Services

### horde node (`horde serve`)

The long-running process that spawns and manages agent subprocesses. Runs
in `master` or `slave` mode (see `--mode`). Listens on `server.port`.

### horde agent subprocess (`horde agent --name <name> --socket <path>`, hidden)

A subprocess of the horde binary that hosts a single ADK agent, serving it
over HTTP on a unix domain socket. Spawned by the node; not intended to be
invoked directly. The node reads a `spawn_ready` NDJSON handshake on the
subprocess's stdout to discover the socket path, then reverse-proxies
invocation requests to `POST /invoke` on that socket.

### horde aap-mock (`horde aap-mock`, hidden)

The Agent Adapter Protocol (AAP) mock adapter — a conformance fixture and
reference adapter that speaks the AAP stdio binding on stdin/stdout. See the
[AAP spec](spec/agent-adapter-protocol-v1.md) and
[AAP concept](knowledgebase/concepts/agent-adapter-protocol.md).

**AAP environment variables** (read by adapters, not by the `HORDE_HORDE_*`
config loader):

| Variable | Legacy alias | Default | Description |
|----------|--------------|---------|-------------|
| `AAP_TRANSPORT` | `AGENTD_AAP_TRANSPORT` | `stdio` | Binding selector: `stdio` or `websocket`. |
| `AAP_WS_URL` | `AGENTD_AAP_WS_URL` | (unset) | WebSocket URL for the `websocket` binding. |

Canonical `AAP_*` names take precedence over the deprecated `AGENTD_AAP_*`
aliases when both are set.

### External AAP adapter (real coding agent)

A real external agent (e.g. the `pi` coding agent via the pi-aap adapter) is a
subprocess declared under `agents.<name>` with `kind: aap` — see [AAP agent
declarations](#aap-agent-declarations-agentsname) and
[`docs/examples/pi-agent.yaml`](examples/pi-agent.yaml). It is not a horde
binary; the adapter lives in its own repository and speaks the AAP stdio
binding.

**Test-only variable** (read by `internal/server` tests, not by the config
loader):

| Variable | Default | Description |
|----------|---------|-------------|
| `HORDE_TEST_PI_ADAPTER` | (unset) | Path to the pi-aap adapter's built entry point (`packages/pi-adapter/dist/index.js`). When set, `TestSpawnAAPAgent_PiAdapter` drives the real adapter through the host handshake; the test skips when unset. |

### horde TUI (`horde`)

The terminal interface. A pure client of the node API: it probes
`GET /api/v1/health` at `localhost:<server.port>` and shows a 60-second
retry countdown (with an immediate-retry key) when no node is reachable.
It does not start a node.

## Integration environment (Docker)

`docker/docker-compose.yml` defines three services from one image:

| Service  | Mode    | Port            | Connects to        |
|---------|---------|-----------------|--------------------|
| `master` | master  | `13420:13420`   | —                  |
| `slave1`| slave   | `13421:13420`   | `master:13420`     |
| `slave2`| slave   | `13422:13420`   | `master:13420`     |

Run with: `task docker:up` (or `docker compose -f docker/docker-compose.yml up`).
