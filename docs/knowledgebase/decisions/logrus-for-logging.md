---
type: Decision
title: logrus for logging
description: Use github.com/sirupsen/logrus for structured logging.
tags: [decision, logging, dependencies]
timestamp: 2026-07-08T00:00:00Z
---

# Context

The project needed a logging library. The reference config stack
(`plantd/core/config`) and the `plantd/identity` extension both use logrus
for the fatal-on-config-error path.

# Decision

Use `github.com/sirupsen/logrus` throughout (server, app, cmd). The
formatter (`text` or `json`) and level come from the
[configuration](/docs/knowledgebase/concepts/configuration.md) `log` section.

# Consequences

Consistent with the broader plantd ecosystem. Loki support is explicitly
*not* included — this project does not need it.