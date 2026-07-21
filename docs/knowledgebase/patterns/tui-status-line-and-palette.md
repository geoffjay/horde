---
type: Pattern
title: TUI status line and command palette
description: A configurable right-aligned status line of blocks, plus a ctrl+p command palette overlaid on a dimmed background.
tags: [pattern, tui, bubbletea, lipgloss]
timestamp: 2026-07-10T00:00:00Z
---

# Pattern

The TUI (`internal/app`) has two composable UI conventions:

1. **Status line** — a configurable, right-aligned bottom bar built from ordered
   *blocks* joined by a separator. Blocks can be added/removed at runtime.
2. **Command palette** — a `ctrl+p` overlay listing available commands, with
   search-to-filter, arrow/enter navigation, and the rest of the screen dimmed
   behind it.

Both are pure client-side view concerns; they follow
[the TUI consumes the node API](../decisions/tui-uses-node-api.md) and never
reach into `internal/server`.

# Files

* `internal/app/statusline.go` — `StatusLine`, `StatusBlock`, and the default
  `node` and `commands` blocks.
* `internal/app/palette.go` — the `palette` state, the `command` list, key
  handling (`handlePaletteKey`), and dialog rendering.
* `internal/app/app.go` — `Model` wires in `status *StatusLine` and `pal
  palette`; `View` composites the overlay; `paint` implements the dimming.

# Status line

`StatusBlock{Name, Render}` produces one segment. `Render(m) string` returns the
segment's (styled) text; returning `""` omits the block and its separator.
`StatusLine` joins the non-empty blocks with a configurable `Separator`
(default `>`) and right-aligns the result to the terminal width.

```go
s := NewStatusLine()          // Separator defaults to ">"
s.Add(nodeStatusBlock())      // "● master · n1 · 2 agents" / "● disconnected"
s.Add(commandsBlock())        // "ctrl+p commands" (chord bold, label faint)
s.Remove("node")              // blocks are removable by name

// Render: "● master · n1 · 2 agents > ctrl+p commands", right-aligned in width.
line := s.Render(m, width)
```

`DefaultStatusLine()` is what `New()` installs. The `node` block shows a
connection dot (green connected / red disconnected) followed by a faint summary
(`nodeSummary`: mode, node id, agent count). The `commands` block renders the
`ctrl+p` chord in bold and the "commands" label in the same faint gray as the
separator so it does not compete with the chord.

# Command palette

`palette{open, query, cursor}` holds the overlay state on the `Model`. `ctrl+p`
opens it; while open, `handleKey` routes everything to `handlePaletteKey`:
`esc`/`ctrl+p` close, `up`/`down` (or `ctrl+k`/`ctrl+j`) move the cursor,
`enter` runs the selection, `backspace` edits the query, and any other
printable keystroke is appended to the search query. Commands are built
per-state by `Model.commands()` and filtered by a case-insensitive label match
in `filteredCommands()`.

The dialog renders a search field with a reverse-video block cursor (a faint
`Search` placeholder when empty) and the filtered command list via
`renderCommandRows`. Lists longer than `paletteMaxRows` scroll: `paletteWindow`
returns the `[start, end)` slice that keeps the cursor visible, and `↑ / ↓ more`
hints mark hidden rows above/below.

# Layout and spacing

`Model.fill` pins the body to the top and the status line to the bottom, padding
the gap so the view fills the terminal height, then insets the whole block by
`edgePad` (one cell) on the left, right, and bottom — the top is left flush.
`Model.innerWidth()` (width minus the two side insets) is the width the status
line is right-aligned to. Note the `+1` in the gap calculation: joining body and
footer with N newlines produces N-1 blank rows between them.

The connected body is a **two-pane layout** (`renderPanes`): a full-height left
navigation sidebar, a one-column vertical divider, and the detail pane, joined
with `lipgloss.JoinHorizontal(lipgloss.Top, …)`. All three columns are wrapped
in `lipgloss.NewStyle().Width(w).Height(H)` and padded to the **same** height
`H = m.height − edgePad − footerH − titleRows` so the columns align and `fill`'s
gap math resolves to no interior blank rows (keeping the footer pinned). The
sidebar model itself lives in `internal/app/sidebar.go`; see
[TUI sidebar navigation](tui-sidebar-navigation.md). Because the sidebar
subsumes navigation, the palette's navigation commands (Nodes, Agents, Activity,
Logs, Switch Project) now *jump the sidebar* — they move the sidebar cursor and
apply the selection rather than pushing a breadcrumb.

**Alt-screen is required for this layout.** `View` returns its views via
`altView`, which sets `tea.View.AltScreen = true`. In inline (non-alt) mode
bubbletea sizes the frame to `content.Height()` and trims trailing blank
lines, so the bottom edge inset (a blank final row) would silently disappear.
Alt-screen uses a fixed full-terminal-height buffer, so trailing blank rows are
real cells that render and the top-flush / full-height model is anchored to the
terminal instead of relying on inline scrolling.

# Dimming the background (the `paint` trick)

`View` composites two layers with lipgloss's `Compositor`: the dimmed background
at Z 0 and the dialog at Z 1. Uniform dimming relies on the background carrying
**no** inner ANSI color/reset escapes, so a single faint wrapper applies evenly.

`Model.paint(render, s)` enforces that: it applies a style's bound `Render`
method normally, but returns `s` **unstyled** while `pal.open` is true. Every
styled site in the background (title, mode, status blocks, retry warning, the
sidebar cursor highlight, and the pane divider) goes through `paint`; the palette
dialog itself does **not** — it renders styles directly because it is the bright
foreground layer. The pane wrappers must not set a `Background()` (only
`Width`/`Height` padding, which is spaces) or the dimming would show a bright
band.

```go
// background site — dims when the palette is open:
title := m.paint(titleStyle.Render, "horde")

// View: composite dimmed bg + bright dialog
dimmed := lipgloss.NewStyle().Faint(true).Foreground(dimColor).Render(background)
comp := lipgloss.NewCompositor(
    lipgloss.NewLayer(dimmed),
    lipgloss.NewLayer(m.renderPalette()).X(x).Y(y).Z(1),
)
```

Note `paint` takes a `func(...string) string` (a bound `Style.Render`), not a
`lipgloss.Style` value — passing the ~648-byte `Style` by value trips
gocritic's `hugeParam`.

# Rationale

Blocks keep the status line open to extension (a new block is a struct with a
render func, added in `DefaultStatusLine`) without touching layout code. The
palette gives a single discoverable entry point (`ctrl+p`) for commands instead
of a growing footer of key hints. The plain-when-dimmed `paint` approach avoids
cell-level ANSI manipulation while still dimming arbitrary background content
uniformly.
