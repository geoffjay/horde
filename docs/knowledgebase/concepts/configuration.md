---
type: Concept
title: Configuration
description: The layered config system, search paths, and env overrides.
tags: [config, core]
timestamp: 2026-07-08T00:00:00Z
---

horde uses a layered configuration system adapted from
`github.com/geoffjay/plantd/core/config` (vendored into `internal/config/`
with the `HORDE_` prefix).

# Layers

1. **Defaults** baked into `internal/config/horde.go`.
2. **Config file** (`horde.yaml`, `horde.json`, or `horde.toml`) searched in
   `./`, `~/.config/horde/`, and `/etc/horde/`.
3. **Environment variables** prefixed `HORDE_*` (dots become
   underscores), e.g. `HORDE_SERVER_PORT=14000`.

An explicit config file path can be set via `HORDE_CONFIG`.

# Extension pattern

The generic loader (`config.go`) provides `LoadConfigWithDefaults` and
`LoadConfig`. The horde-specific `Config` struct (`horde.go`) embeds the
common pieces (`Log`, `Service`) and adds app-specific sections (`Env`,
`Mode`, `Server`, `Cluster`). See
[config extension pattern](/docs/knowledgebase/patterns/config-extension.md).

# Keys

See the [environment](environment.md) concept for the full key/env-var table.

# Formats

YAML, JSON, and TOML are all supported. Test fixtures in
`internal/config/testdata/` exercise all three.
