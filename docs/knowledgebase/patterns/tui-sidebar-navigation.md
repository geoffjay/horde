---
type: Pattern
title: TUI sidebar navigation
description: A full-height left sidebar of expandable groups (Nodes, Users, Agents, Teams, Projects) plus feed leaves, driving a right-hand detail pane; the flattened-row model, two-zone focus, and applySelection.
tags: [pattern, tui, bubbletea, lipgloss, navigation]
timestamp: 2026-07-20T00:00:00Z
---

# Pattern

The TUI (`internal/app`) navigates by a **full-height left sidebar** of top-level
groups the user expands and selects from, with the selected entity/feed rendered
in a right-hand **detail pane**. This replaced the earlier breadcrumb
drill-down (`m.crumbs` + `pushView`/`popView`); there is no crumb stack.

The sidebar lists five expandable groups — **Nodes, Users, Agents, Teams,
Projects** — followed by two standalone feed leaves, **Activity** and **Logs**.
Three groups have real backing data (Nodes, Agents, Projects). **Teams are
derived** from projects (each `client.Project` carries a `Team`; there is no team
API), one entry per project. **Users is a stub** — per-user accounts require
authentication that does not exist yet, so it renders a "not available" notice.

# Files

* `internal/app/sidebar.go` — the sidebar state (`sidebar`, `groupID`,
  `sidebarKind`, `sidebarRow`, `focus`), row flattening (`sidebarRows`), key
  handling (`handleSidebarKey`), selection (`applySelection`), rendering
  (`renderSidebar`), and the `jumpToGroup`/`jumpToChild` shortcut helpers.
* `internal/app/navigation.go` — the transient detail drill (`detailEnter`,
  `pushDetail`, `popDetail`, bounded `detailBack`) plus the `selectedProjectIndex`
  / `selectedAgent` / `visibleAgents` helpers reused by the detail views.
* `internal/app/app.go` — `renderPanes` (two-column layout) and `handleKey`
  focus routing; `Model` holds `sidebar`, `focus`, and `detailBack`.

# Flattened-row model

`m.sidebarRows()` rebuilds a flat, cursor-navigable `[]sidebarRow` every
render/navigation from live data and the `expanded` set: each top-level entry in
`topLevelOrder`, with the children of an expanded group spliced in below its
header. `m.sidebar.cursor` indexes this list (clamped by `moveSidebarCursor`);
`groupRowIndex` finds a header's index. Keeping the model flat means a single
cursor and a single `sidebarLen` clamp regardless of what is expanded.

The `m.view` enum is retained as the **detail-pane selector**: every existing
`render*View` renders unchanged as detail content. The sidebar's job is to set
`m.view` (plus `selectedProjectID`/`selectedAgentID` and any stream) — done in
one place, `applySelection(row)`, which first tears down active streams and
resets transient detail state (drill, cursors, action error). Centralizing
teardown there is what prevents the Activity/agent SSE streams from leaking when
the selection changes.

# Two-zone focus

`m.focus` is `focusSidebar` or `focusDetail`. `handleKey` routes non-invoke keys
by focus: `handleSidebarKey` moves the sidebar cursor, expands/collapses groups
(enter/→/←), and selects children/leaves (handing focus to the detail pane);
`handleDetailKey` drives the detail list cursor, fires the view action keys
(`a`/`d`, the `ctrl+*` lifecycle keys), and drills with `detailEnter`. `esc`/tab
in the detail pane pops the transient `detailBack` drill (project → agent →
invoke, depth ≤ 2) or, when empty, returns focus to the sidebar. List renderers
apply the `selStyle` highlight only when `m.focus == focusDetail`, so the active
highlight lives in whichever pane has focus.

# Rationale

One sidebar makes the whole system visible at a glance and unifies navigation:
nodes, agents, teams, and projects are peers instead of being reachable only by
drilling through projects or via palette commands. Reusing the `m.view` enum kept
the change to navigation + layout without rewriting the detail renderers. See
also [TUI status line and command palette](tui-status-line-and-palette.md) for
the two-pane layout math and the `paint` dimming discipline (which now also
covers the sidebar highlight and divider).
