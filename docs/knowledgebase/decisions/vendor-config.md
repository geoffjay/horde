---
type: Decision
title: Vendor plantd config into internal/config
description: Avoid pulling plantd/core as a dependency by vendoring the config loader.
tags: [decision, config, dependencies]
timestamp: 2026-07-08T00:00:00Z
---

# Context

The project's configuration system is an existing implementation used across
several projects, living in `github.com/geoffjay/plantd/core/config`. That
package has no standalone module — it is part of the `plantd/core` module.

Depending on `plantd/core` would pull a large, unrelated module into horde's
`go.sum`.

# Decision

Vendor the config loader into `internal/config/`, adapting the env prefix
from `PLANTD_` to `HORDE_` and the search paths to `~/.config/horde/` and
`/etc/horde/`. Follow the same extension pattern as `plantd/identity` for the
horde-specific `Config` struct (see
[config extension pattern](/docs/knowledgebase/patterns/config-extension.md)).

# Consequences

No dependency on plantd/core. The loader stays small (~150 lines) and the
horde extension is co-located. A missing config file is no longer fatal —
defaults and env overrides still apply.