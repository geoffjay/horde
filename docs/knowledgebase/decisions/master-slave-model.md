---
type: Decision
title: Master/slave cluster model
description: Distributed topology where one node leads and others follow without blocking.
tags: [decision, architecture, distributed]
timestamp: 2026-07-08T00:00:00Z
---

# Context

horde needs to support both a standalone single-host mode and a multi-user
distributed mode where multiple hosts form a cluster.

# Decision

Adopt a master/slave (leader/follower) topology:

* `master` (default) is the central hub and source of truth.
* `slave` connects to a master but is *not blocked* by that connection for
  local functionality — local agents run immediately and the leader
  connection is established in the background.

The relationship is largely invisible to the user on each system.

# Consequences

The `--mode` flag and the `mode` config key select the role. Slaves without a
configured leader run standalone with a warning. The real cluster transport
(health, registration) is being built in Phase 2 over the
[HTTP + SSE API](http-api-transport.md) — see the
[Phase 2 plan](/docs/knowledgebase/plans/phase-2-server-api.md) for the
slave↔master contract.
