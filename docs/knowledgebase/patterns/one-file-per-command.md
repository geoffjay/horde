---
type: Pattern
title: One file per cobra command
description: Each cobra command lives in its own file in the cmd/ package.
tags: [pattern, cli, cobra]
timestamp: 2026-07-08T00:00:00Z
---

# Pattern

Each cobra command lives in its own file within the `cmd/` package. The root
command is defined in `cli.go` along with `Execute()`; subcommands are added
via `rootCmd.AddCommand(...)` in each command file.

# Files

* `cmd/cli.go` — root command + `Execute()`.
* `cmd/tui.go` — default action (launches the TUI).
* `cmd/serve.go` — `serve` subcommand.
* `cmd/agent.go` — hidden `agent` subcommand (agent host).
* `cmd/daemonize.go` — `--daemonize` helper.

# Example

```go
// cmd/serve.go
var serveCmd = &cobra.Command{
    Use:   "serve",
    RunE:  runServe,
}

func init() { rootCmd.AddCommand(serveCmd) }

func runServe(cmd *cobra.Command, _ []string) error { ... }
```

# Rationale

Mirrors the layout used by `~/Projects/otter`. Keeps each command's flags,
help text, and handler co-located and easy to find.