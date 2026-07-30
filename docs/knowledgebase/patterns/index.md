# Patterns

Recurring implementation patterns used in the project.

* [One file per cobra command](one-file-per-command.md) - cmd/ package layout.
* [Config extension pattern](config-extension.md) - embedding generic config + app-specific sections.
* [Subprocess agent hosting](subprocess-agent-hosting.md) - the binary hosts its own agents.
* [TUI status line and command palette](tui-status-line-and-palette.md) - configurable status blocks + a ctrl+p palette over a dimmed background.
* [TUI sidebar navigation](tui-sidebar-navigation.md) - full-height left sidebar of expandable groups + a detail pane; the flattened-row model, focus zones, and `applySelection`.
* [No phase/milestone references in code](no-phase-references.md) - name and describe code by what it is, not by the phase/plan/issue that introduced it (file names, comments, identifiers).
* [Unit / integration test split via build tags](unit-integration-test-split.md) - `task test` is unit-only + deterministic; subprocess/network/timing tests carry `//go:build integration` and run via `task test:integration`.
* [Principal middleware + X-Horde-User echo-trust seam](principal-middleware-and-echo-trust.md) - `resolvePrincipal` labels every request anon/user/node (never rejects); `requireUser` + `authorizeProject` gate mutations; cross-node identity is echoed as `X-Horde-User` and honored only for a node principal. Opt-in — disabled is a true no-op.
* [Three-way convergence for file-granular sync](three-way-convergence.md) - compare the authority's digest, a node's synced_digest, and the on-disk digest to classify every file path into exactly one convergence action (KSP §5.1). The correctness core of KB sync; the stage distinction is a thin wrapper over one table.
