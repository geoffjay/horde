package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// pickerWidth is the content-box width of the picker dialog (border adds 2).
const pickerWidth = 64

// pickerPadX / pickerPadY are the picker dialog's inner padding.
const (
	pickerPadX = 2
	pickerPadY = 1
)

// pickerInner is the usable content width inside the border and horizontal
// padding (2 border chars + 2*pickerPadX padding chars).
const pickerInner = pickerWidth - pickerPadX*2 - 2

// pickerMaxRows caps how many rows are shown at once.
const pickerMaxRows = 8

// pickerItem is one selectable row: id is passed to onSelect, label is the
// primary text, right is optional right-aligned secondary text, and dot is an
// optional leading status glyph.
type pickerItem struct {
	id    string
	label string
	right string
	dot   string
}

// listPicker is a reusable single-select overlay: a title, a list of items, and
// a callback invoked with the chosen item. It serves project selection, assign
// targets, and agent attachment.
type listPicker struct {
	open     bool
	cursor   int
	title    string
	items    []pickerItem
	onSelect func(m *Model, item pickerItem) tea.Cmd
}

// openPicker shows the picker overlay with the given title, items, and select
// callback, cursor on the first item.
func (m *Model) openPicker(title string, items []pickerItem, onSelect func(*Model, pickerItem) tea.Cmd) {
	m.picker = listPicker{open: true, title: title, items: items, onSelect: onSelect}
}

// closePicker hides the picker overlay.
func (m *Model) closePicker() {
	m.picker = listPicker{}
}

// openSwitchProjectPicker opens the picker over projects; selecting one drills
// into its detail view (the palette's "Switch Project" command).
func (m *Model) openSwitchProjectPicker() {
	items := make([]pickerItem, 0, len(m.projects))
	for _, p := range m.projects {
		items = append(items, pickerItem{id: p.ID, label: p.Name, right: p.State, dot: stateDot(p.State)})
	}
	m.openPicker("Select Project", items, func(m *Model, it pickerItem) tea.Cmd {
		m.goHome()
		m.pushView(viewProjectDetail, it.id, it.label)
		return nil
	})
}

// openAssignProjectPicker opens the picker over active projects; selecting one
// attaches the given agent to it (the Agents-view assign action).
func (m *Model) openAssignProjectPicker(agentID string) {
	var items []pickerItem
	for _, p := range m.projects {
		if p.State != stateActive {
			continue
		}
		items = append(items, pickerItem{id: p.ID, label: p.Name, right: p.State, dot: stateDot(p.State)})
	}
	m.openPicker("Assign to project", items, func(m *Model, it pickerItem) tea.Cmd {
		return m.attachAgentCmd(it.id, agentID)
	})
}

// openAgentPicker opens the picker over unassigned agents; selecting one
// attaches it to the given project (the project-detail assign action).
func (m *Model) openAgentPicker(projectID string) {
	var items []pickerItem
	for _, a := range m.agents {
		if m.contexts[a.ID].Project != "" {
			continue // only agents not already on a project
		}
		items = append(items, pickerItem{id: a.ID, label: a.Name, right: a.ID, dot: greenDot()})
	}
	m.openPicker("Attach agent", items, func(m *Model, it pickerItem) tea.Cmd {
		return m.attachAgentCmd(projectID, it.id)
	})
}

// clampPickerCursor keeps the cursor within the bounds of the item list.
func (m *Model) clampPickerCursor() {
	n := len(m.picker.items)
	switch {
	case n == 0 || m.picker.cursor < 0:
		m.picker.cursor = 0
	case m.picker.cursor >= n:
		m.picker.cursor = n - 1
	}
}

// movePickerCursor moves the highlighted item by delta, clamped.
func (m *Model) movePickerCursor(delta int) {
	m.picker.cursor += delta
	m.clampPickerCursor()
}

// runSelectedItem invokes the picker's callback with the highlighted item and
// closes the overlay.
func (m *Model) runSelectedItem() (tea.Model, tea.Cmd) {
	if len(m.picker.items) == 0 {
		m.closePicker()
		return m, nil
	}
	it := m.picker.items[m.picker.cursor]
	onSelect := m.picker.onSelect
	m.closePicker()
	if onSelect != nil {
		return m, onSelect(m, it)
	}
	return m, nil
}

// handlePickerKey handles key presses while the picker is open: esc closes it,
// arrows move the cursor, enter selects.
func (m *Model) handlePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyQuit:
		m.closePicker()
		m.quitting = true
		return m, tea.Quit
	case keyEsc:
		m.closePicker()
		return m, nil
	case "up", "ctrl+k":
		m.movePickerCursor(-1)
		return m, nil
	case keyDown, "ctrl+j":
		m.movePickerCursor(1)
		return m, nil
	case keyEnter:
		return m.runSelectedItem()
	}
	return m, nil
}

// renderPicker builds the picker dialog. It is composited as its own layer over
// the dimmed background, so it uses styles directly rather than Model.paint.
func (m *Model) renderPicker() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

	var b strings.Builder
	b.WriteString(titleStyle.Render(m.picker.title))
	b.WriteString("\n\n")

	if len(m.picker.items) == 0 {
		b.WriteString(dimLabelStyle.Render("(nothing to choose)"))
		b.WriteString("\n\n")
	} else {
		b.WriteString(m.renderPickerRows())
		b.WriteString("\n\n")
	}

	b.WriteString(keyHintStyle.Render("↑↓"))
	b.WriteString(" " + dimLabelStyle.Render("choose"))
	b.WriteString(" " + keyHintStyle.Render("·") + " ")
	b.WriteString(keyHintStyle.Render("enter"))
	b.WriteString(" " + dimLabelStyle.Render("select"))
	b.WriteString(" " + keyHintStyle.Render("·") + " ")
	b.WriteString(keyHintStyle.Render("esc"))
	b.WriteString(" " + dimLabelStyle.Render("cancel"))

	box := lipgloss.NewStyle().
		Width(pickerWidth).
		Padding(pickerPadY, pickerPadX).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
	return box.Render(b.String())
}

// renderPickerRows renders the item list, scrolled to keep the cursor visible.
// The highlighted row has a filled background spanning the dialog's inner width.
func (m *Model) renderPickerRows() string {
	selStyle := lipgloss.NewStyle().
		Width(pickerInner).
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("255"))

	start, end := paletteWindow(len(m.picker.items), m.picker.cursor, pickerMaxRows)

	var rows []string
	if start > 0 {
		rows = append(rows, dimLabelStyle.Render("↑ more"))
	}
	for i := start; i < end; i++ {
		it := m.picker.items[i]
		label := it.label
		if it.dot != "" {
			label = it.dot + " " + it.label
		}
		row := spread(pickerInner, label, dimLabelStyle.Render(it.right))
		if i == m.picker.cursor {
			row = selStyle.Render(row)
		}
		rows = append(rows, row)
	}
	if end < len(m.picker.items) {
		rows = append(rows, dimLabelStyle.Render("↓ more"))
	}
	return strings.Join(rows, "\n")
}
