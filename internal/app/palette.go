package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	// paletteWidth is the width of the command dialog's content box (the
	// border adds two more columns).
	paletteWidth = 64
	// palettePadX / palettePadY are the dialog's horizontal / vertical inner
	// padding.
	palettePadX = 2
	palettePadY = 1
	// paletteInner is the usable content width inside the border and
	// horizontal padding (2 border chars + 2*palettePadX padding chars).
	paletteInner = paletteWidth - palettePadX*2 - 2
	// centerDivisor halves free space to center the dialog on each axis.
	centerDivisor = 2
	// paletteMaxRows caps how many command rows are shown at once; longer
	// lists scroll to keep the cursor visible.
	paletteMaxRows = 8
)

// Color values for the palette dialog.
var (
	// dimGray is used for the most dimmed text (footer labels like
	// "choose", "confirm", "cancel", "Type to filter").
	dimGray = lipgloss.Color("240")
	// lessDimGray is used for key glyphs (↑↓, enter, esc) that should be
	// gray but more visible than the labels.
	lessDimGray = lipgloss.Color("248")
	// brightGreen is the color of the ">" prompt in the search field.
	brightGreen = lipgloss.Color("46")
)

// palette is the state of the ctrl+p command overlay: whether it is open, the
// current search query, and the index of the highlighted command within the
// filtered list.
type palette struct {
	open   bool
	query  string
	cursor int
}

// command is one entry in the palette: a label, the key chord that also
// triggers it, and the action to run when it is selected. run returns the
// tea.Cmd to dispatch (it may mutate the model, e.g. to set quitting).
type command struct {
	label string
	key   string
	run   func(m *Model) tea.Cmd
}

// baseCommands returns the fixed set of commands shown in the palette. The
// list does not change with the current view or selection, so the dialog
// remains stable while it is open. Lifecycle actions (pause/resume/finish/
// assign) are not included; they are available as direct keys in the
// project detail view.
func (m *Model) baseCommands() []command {
	return []command{
		{
			label: "Refresh",
			key:   keyCtrlR,
			run:   func(m *Model) tea.Cmd { return m.loadNode },
		},
		{
			label: "Select Cluster",
			key:   "ctrl+l",
			run: func(m *Model) tea.Cmd {
				m.goCluster()
				return nil
			},
		},
		{
			label: "New Project",
			key:   "ctrl+n",
			run: func(m *Model) tea.Cmd {
				m.openForm()
				return nil
			},
		},
		{
			label: "Agents",
			run: func(m *Model) tea.Cmd {
				m.goAgents()
				return nil
			},
		},
		{
			label: "New Agent",
			run: func(m *Model) tea.Cmd {
				m.openAgentForm()
				return nil
			},
		},
		{
			label: "Cluster Activity",
			run: func(m *Model) tea.Cmd {
				return m.goEvents()
			},
		},
		{
			label: "Switch Project",
			key:   keyCtrlP,
			run: func(m *Model) tea.Cmd {
				m.openSwitchProjectPicker()
				return nil
			},
		},
		{
			label: "Quit",
			key:   keyCtrlQ,
			run: func(m *Model) tea.Cmd {
				m.quitting = true
				return tea.Quit
			},
		},
	}
}

// retryCommands returns the commands shown when disconnected: just Retry and
// Quit.
func (m *Model) retryCommands() []command {
	return []command{
		{
			label: "Retry now",
			key:   keyCtrlR,
			run: func(m *Model) tea.Cmd {
				m.retryIn = 0
				return m.connect
			},
		},
		{
			label: "Quit",
			key:   keyCtrlQ,
			run: func(m *Model) tea.Cmd {
				m.quitting = true
				return tea.Quit
			},
		},
	}
}

// commands returns every command available in the current state. When
// connected the five base commands are shown; when disconnected Retry and
// Quit are shown.
func (m *Model) commands() []command {
	if m.connected {
		return m.baseCommands()
	}
	return m.retryCommands()
}

// filteredCommands returns the commands whose label contains the current query
// (case-insensitive). An empty query matches everything.
func (m *Model) filteredCommands() []command {
	all := m.commands()
	if m.pal.query == "" {
		return all
	}
	q := strings.ToLower(m.pal.query)
	var out []command
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.label), q) {
			out = append(out, c)
		}
	}
	return out
}

// openPalette shows the overlay with a cleared query and the cursor on the
// first command.
func (m *Model) openPalette() {
	m.pal = palette{open: true}
}

// closePalette hides the overlay and resets its transient state.
func (m *Model) closePalette() {
	m.pal = palette{}
}

// clampPaletteCursor keeps the cursor within the bounds of the filtered list.
func (m *Model) clampPaletteCursor() {
	n := len(m.filteredCommands())
	switch {
	case n == 0 || m.pal.cursor < 0:
		m.pal.cursor = 0
	case m.pal.cursor >= n:
		m.pal.cursor = n - 1
	}
}

// movePaletteCursor moves the highlighted command by delta, clamped to the
// filtered list.
func (m *Model) movePaletteCursor(delta int) {
	m.pal.cursor += delta
	m.clampPaletteCursor()
}

// runSelectedCommand executes the highlighted command (if any), closes the
// palette, and returns its tea.Cmd.
func (m *Model) runSelectedCommand() (tea.Model, tea.Cmd) {
	cmds := m.filteredCommands()
	if len(cmds) == 0 {
		return m, nil
	}
	selected := cmds[m.pal.cursor]
	m.closePalette()
	return m, selected.run(m)
}

// handlePaletteKey handles key presses while the palette overlay is open:
// esc/ctrl+p close it, arrows move the cursor, enter runs the selection, and
// any other printable input edits the search query.
func (m *Model) handlePaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyQuit:
		m.quitting = true
		return m, tea.Quit
	case keyEsc, keyCtrlP:
		m.closePalette()
		return m, nil
	case "up", "ctrl+k":
		m.movePaletteCursor(-1)
		return m, nil
	case keyDown, "ctrl+j":
		m.movePaletteCursor(1)
		return m, nil
	case keyEnter:
		return m.runSelectedCommand()
	case keyBackspace:
		if m.pal.query != "" {
			m.pal.query = m.pal.query[:len(m.pal.query)-1]
			m.clampPaletteCursor()
		}
		return m, nil
	}

	// Any other printable keystroke is appended to the search query.
	if msg.Text != "" {
		m.pal.query += msg.Text
		m.clampPaletteCursor()
	}
	return m, nil
}

// dimLabelStyle styles text as dimmed gray (for "Type to filter", "choose",
// "confirm", "cancel").
var dimLabelStyle = lipgloss.NewStyle().Foreground(dimGray).Faint(true)

// keyHintStyle styles text as gray but less dimmed (for ↑↓, enter, esc key
// glyphs).
var keyHintStyle = lipgloss.NewStyle().Foreground(lessDimGray)

// renderPalette builds the command dialog shown while the palette is open. It
// is composited as its own layer over the dimmed background, so it uses styles
// directly rather than Model.paint.
func (m *Model) renderPalette() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

	var b strings.Builder
	b.WriteString(titleStyle.Render("Commands"))
	b.WriteString("\n\n")

	// Search field: a bright green ">" prompt followed by the query and a
	// block cursor, or a dimmed "Type to filter" placeholder when empty.
	prompt := lipgloss.NewStyle().Foreground(brightGreen).Render(">")
	b.WriteString(prompt + " ")
	cursor := lipgloss.NewStyle().Reverse(true).Render(" ")
	b.WriteString(m.pal.query + cursor)
	if m.pal.query == "" {
		b.WriteString(" " + dimLabelStyle.Render("Type to filter"))
	}
	b.WriteString("\n\n")

	b.WriteString(m.renderCommandRows())
	b.WriteString("\n\n")

	// Footer hints: "↑↓ choose · enter confirm · esc cancel" with the key
	// glyphs in lessDimGray and the labels in dimGray.
	b.WriteString(keyHintStyle.Render("↑↓"))
	b.WriteString(" " + dimLabelStyle.Render("choose"))
	b.WriteString(" " + keyHintStyle.Render("·") + " ")
	b.WriteString(keyHintStyle.Render("enter"))
	b.WriteString(" " + dimLabelStyle.Render("confirm"))
	b.WriteString(" " + keyHintStyle.Render("·") + " ")
	b.WriteString(keyHintStyle.Render("esc"))
	b.WriteString(" " + dimLabelStyle.Render("cancel"))

	box := lipgloss.NewStyle().
		Width(paletteWidth).
		Padding(palettePadY, palettePadX).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
	return box.Render(b.String())
}

// renderCommandRows renders the filtered command list, scrolled to keep the
// cursor visible and with "↑ / ↓ more" hints when rows are hidden above or
// below the visible window. The highlighted row is shown with a filled
// background spanning the dialog's inner width.
func (m *Model) renderCommandRows() string {
	cmds := m.filteredCommands()
	if len(cmds) == 0 {
		return dimLabelStyle.Render("(no matching commands)")
	}

	selStyle := lipgloss.NewStyle().
		Width(paletteInner).
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("255"))

	start, end := paletteWindow(len(cmds), m.pal.cursor, paletteMaxRows)

	var rows []string
	if start > 0 {
		rows = append(rows, dimLabelStyle.Render("↑ more"))
	}
	for i := start; i < end; i++ {
		c := cmds[i]
		selected := i == m.pal.cursor
		key := c.key
		if !selected {
			key = dimLabelStyle.Render(c.key)
		}
		row := spread(paletteInner, c.label, key)
		if selected {
			row = selStyle.Render(row)
		}
		rows = append(rows, row)
	}
	if end < len(cmds) {
		rows = append(rows, dimLabelStyle.Render("↓ more"))
	}
	return strings.Join(rows, "\n")
}

// paletteWindow returns the [start, end) slice of command indices to display
// so that cursor stays visible within maxRows rows.
func paletteWindow(total, cursor, maxRows int) (start, end int) {
	if total <= maxRows {
		return 0, total
	}
	start = cursor - maxRows/centerDivisor
	if start < 0 {
		start = 0
	}
	end = start + maxRows
	if end > total {
		end = total
		start = end - maxRows
	}
	return start, end
}

// spread lays out left and right within width, pushing right to the far edge
// with at least one space of gap between them.
func spread(width int, left, right string) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// dialogOffset centers the dialog within the current terminal, clamped so it
// never renders off the top-left edge. Used by the palette, picker, and form
// modal overlays.
func (m *Model) dialogOffset(dialog string) (x, y int) {
	x = (m.width - lipgloss.Width(dialog)) / centerDivisor
	y = (m.height - lipgloss.Height(dialog)) / centerDivisor
	return max(x, 0), max(y, 0)
}
