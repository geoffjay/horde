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
3. **Environment variables** prefixed `HORDE_HORDE_*` (dots become
   underscores), e.g. `HORDE_HORDE_SERVER_PORT=14000`.

An explicit config file path can be set via the `HORDE_HORDE_CONFIG`
environment variable (any extension: `yaml`, `yml`, `json`, `toml`).

### Configuration keys

| Key                              | Default             | Env var                                   | Description                              |
|----------------------------------|---------------------|-------------------------------------------|------------------------------------------|
| `env`                            | `development`       | `HORDE_HORDE_ENV`                         | Environment name.                        |
| `mode`                           | `master`            | `HORDE_HORDE_MODE`                        | Node role: `master` or `slave`.          |
| `server.port`                    | `13420`             | `HORDE_HORDE_SERVER_PORT`                 | Node API listen port.                    |
| `server.agent_command`           | *(current binary)*  | `HORDE_HORDE_SERVER_AGENT_COMMAND`        | Binary used to host agent subprocesses. |
| `server.leader`                  | *(empty)*           | `HORDE_HORDE_SERVER_LEADER`               | Master address for a slave to connect to.|
| `server.read_timeout`            | `30`                | `HORDE_HORDE_SERVER_READ_TIMEOUT`         | API read timeout (seconds).             |
| `server.write_timeout`           | `30`                | `HORDE_HORDE_SERVER_WRITE_TIMEOUT`        | API write timeout (seconds).            |
| `server.idle_timeout`            | `120`               | `HORDE_HORDE_SERVER_IDLE_TIMEOUT`         | API idle timeout (seconds).             |
| `cluster.node_id`                | *(empty)*           | `HORDE_HORDE_CLUSTER_NODE_ID`             | Unique node id within the cluster.       |
| `cluster.discovery_mechanism`    | `static`            | `HORDE_HORDE_CLUSTER_DISCOVERY_MECHANISM` | How nodes find each other (`static`).   |
| `log.formatter`                  | `text`              | `HORDE_HORDE_LOG_FORMATTER`               | Log formatter: `text` or `json`.        |
| `log.level`                      | `info`              | `HORDE_HORDE_LOG_LEVEL`                   | Log level.                               |
| `service.id`                     | `org.horde.Horde`   | `HORDE_HORDE_SERVICE_ID`                  | Service identifier.                      |

## Services

### horde node (`horde serve`)

The long-running process that spawns and manages agent subprocesses. Runs
in `master` or `slave` mode (see `--mode`). Listens on `server.port`.

### horde agent subprocess (`horde agent --name <name>`, hidden)

A subprocess of the horde binary that hosts a single ADK agent. Spawned by
the node; not intended to be invoked directly.

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
