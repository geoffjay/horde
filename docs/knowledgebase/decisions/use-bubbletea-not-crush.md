---
type: Decision
title: Use bubbletea + lipgloss, not crush
description: crush is a standalone app, not a reusable TUI library.
tags: [decision, tui, dependencies]
timestamp: 2026-07-08T00:00:00Z
---

# Context

The project originally listed `github.com/charmbracelet/crush` as the TUI
library. crush is a complete, self-contained AI coding assistant application
(a standalone product), not a reusable TUI framework. Its packages live
under `internal/` and cannot be imported by third parties.

# Decision

Build the TUI on the actual Charm building blocks that crush itself is built
on: `charm.land/bubbletea/v2` (the TUI runtime — model/update/view) and
`charm.land/lipgloss/v2` (styling).

# Consequences

Clean, reusable, idiomatic. The TUI lives in `internal/app/app.go` using
standard bubbletea pointer-receiver models.