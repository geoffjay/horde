# Patterns

Recurring implementation patterns used in the project.

* [One file per cobra command](one-file-per-command.md) - cmd/ package layout.
* [Config extension pattern](config-extension.md) - embedding generic config + app-specific sections.
* [Subprocess agent hosting](subprocess-agent-hosting.md) - the binary hosts its own agents.
* [TUI status line and command palette](tui-status-line-and-palette.md) - configurable status blocks + a ctrl+p palette over a dimmed background.
* [No phase/milestone references in code](no-phase-references.md) - name and describe code by what it is, not by the phase/plan/issue that introduced it (file names, comments, identifiers).
