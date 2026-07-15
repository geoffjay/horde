package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// pickerWidth is the content-box width of the project picker dialog (border
// adds 2).
const pickerWidth = 64

// pickerPadX / pickerPadY are the picker dialog's inner padding.
const (
	pickerPadX = 2
	pickerPadY = 1
)

// pickerInner is the usable content width inside the border and horizontal
// padding (2 border chars + 2*pickerPadX padding chars).
const pickerInner = pickerWidth - pickerPadX*2 - 2

// pickerMaxRows caps how many project rows are shown at once.
const pickerMaxRows = 8

// projectPicker is the state of the project picker overlay: whether it is
// open and the index of the highlighted project.
type projectPicker struct {
	open   bool
	cursor int
}

// openPicker shows the project picker overlay with the cursor on the first
// project. It is opened from the palette's "Switch Project" command.
func (m *Model) openPicker() {
	m.picker = projectPicker{open: true}
}

// closePicker hides the project picker overlay.
func (m *Model) closePicker() {
	m.picker = projectPicker{}
}

// clampPickerCursor keeps the cursor within the bounds of the project list.
func (m *Model) clampPickerCursor() {
	n := len(m.projects)
	switch {
	case n == 0 || m.picker.cursor < 0:
		m.picker.cursor = 0
	case m.picker.cursor >= n:
		m.picker.cursor = n - 1
	}
}

// movePickerCursor moves the highlighted project by delta, clamped.
func (m *Model) movePickerCursor(delta int) {
	m.picker.cursor += delta
	m.clampPickerCursor()
}

// runSelectedProject navigates to the highlighted project's detail view and
// closes the picker.
func (m *Model) runSelectedProject() (tea.Model, tea.Cmd) {
	if len(m.projects) == 0 {
		return m, nil
	}
	p := m.projects[m.picker.cursor]
	m.closePicker()
	m.goHome()
	m.pushView(viewProjectDetail, p.ID, p.Name)
	return m, nil
}

// handlePickerKey handles key presses while the project picker is open:
// esc closes it, arrows move the cursor, enter selects a project.
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
		return m.runSelectedProject()
	}
	return m, nil
}

// renderPicker builds the project picker dialog. It is composited as its own
// layer over the dimmed background, so it uses styles directly rather than
// Model.paint.
func (m *Model) renderPicker() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

	var b strings.Builder
	b.WriteString(titleStyle.Render("Select Project"))
	b.WriteString("\n\n")

	if len(m.projects) == 0 {
		b.WriteString(dimLabelStyle.Render("(no projects)"))
		b.WriteString("\n\n")
	} else {
		b.WriteString(m.renderPickerRows())
		b.WriteString("\n\n")
	}

	// Footer hints: "↑↓ choose · enter open · esc cancel"
	b.WriteString(keyHintStyle.Render("↑↓"))
	b.WriteString(" " + dimLabelStyle.Render("choose"))
	b.WriteString(" " + keyHintStyle.Render("·") + " ")
	b.WriteString(keyHintStyle.Render("enter"))
	b.WriteString(" " + dimLabelStyle.Render("open"))
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

// renderPickerRows renders the project list, scrolled to keep the cursor
// visible. The highlighted row has a filled background spanning the dialog's
// inner width.
func (m *Model) renderPickerRows() string {
	selStyle := lipgloss.NewStyle().
		Width(pickerInner).
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("255"))

	start, end := paletteWindow(len(m.projects), m.picker.cursor, pickerMaxRows)

	var rows []string
	if start > 0 {
		rows = append(rows, dimLabelStyle.Render("↑ more"))
	}
	for i := start; i < end; i++ {
		p := m.projects[i]
		dot := stateDot(p.State)
		label := dot + " " + p.Name
		state := dimLabelStyle.Render(p.State)
		row := spread(pickerInner, label, state)
		if i == m.picker.cursor {
			row = selStyle.Render(row)
		}
		rows = append(rows, row)
	}
	if end < len(m.projects) {
		rows = append(rows, dimLabelStyle.Render("↓ more"))
	}
	return strings.Join(rows, "\n")
}
