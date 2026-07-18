package app

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/internal/client"
)

// Form field indices for the new-project modal, in display order.
const (
	formFieldName = iota
	formFieldWorkspace
	formFieldGoal
	formFieldAgents
	formFieldCount
)

// formWidth is the content-box width of the new-project modal (border adds 2).
const formWidth = 64

// formPadX / formPadY are the modal's inner horizontal / vertical padding.
const (
	formPadX = 2
	formPadY = 1
)

// formInner is the usable content width inside the border and horizontal
// padding (2 border chars + 2*formPadX padding chars).
const formInner = formWidth - formPadX*2 - 2

// formFieldLabels are the display labels for each form field, in index order.
var formFieldLabels = [formFieldCount]string{"Name", "Workspace", "Goal", "Agents"}

// projectForm is the state of the new-project modal overlay: whether it is
// open, which field has focus, and the current value of each field.
type projectForm struct {
	open   bool
	cursor int
	fields [formFieldCount]string
}

// projectActionMsg carries the result of a project lifecycle action
// (create, pause, resume, finish, assign). On success, project is the
// updated or newly created project; err is non-nil on failure.
type projectActionMsg struct {
	project client.Project
	err     error
}

// openForm shows the new-project modal with cleared fields.
func (m *Model) openForm() {
	m.form = projectForm{open: true}
}

// closeForm hides the new-project modal and resets its fields.
func (m *Model) closeForm() {
	m.form = projectForm{}
}

// handleFormKey handles key presses while the new-project form is open:
// esc cancels, enter submits, tab/up/down move between fields, backspace
// deletes, and printable keys append to the active field.
func (m *Model) handleFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyQuit:
		m.closeForm()
		m.quitting = true
		return m, tea.Quit
	case keyEsc:
		m.closeForm()
		return m, nil
	case keyEnter:
		return m.submitForm()
	case "tab", keyDown:
		m.form.cursor = (m.form.cursor + 1) % formFieldCount
		return m, nil
	case "shift+tab", "up":
		m.form.cursor = (m.form.cursor - 1 + formFieldCount) % formFieldCount
		return m, nil
	case keyBackspace:
		f := m.form.fields[m.form.cursor]
		if f != "" {
			m.form.fields[m.form.cursor] = f[:len(f)-1]
		}
		return m, nil
	}

	if msg.Text != "" {
		m.form.fields[m.form.cursor] += msg.Text
	}
	return m, nil
}

// submitForm validates the form and POSTs the new project. It returns a
// tea.Cmd that calls the API; the result arrives as a projectActionMsg.
// If the name field is empty, the form stays open and nothing is sent.
func (m *Model) submitForm() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.form.fields[formFieldName])
	if name == "" {
		return m, nil
	}
	req := client.CreateProjectRequest{
		Name:       name,
		Workspace:  m.form.fields[formFieldWorkspace],
		Goal:       m.form.fields[formFieldGoal],
		AgentNames: parseAgentNames(m.form.fields[formFieldAgents]),
	}
	m.closeForm()
	return m, m.createProjectCmd(req) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
}

// parseAgentNames splits a comma-separated agent names string into a slice,
// trimming whitespace from each entry and dropping empties.
func parseAgentNames(s string) []string {
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// createProjectCmd returns a tea.Cmd that POSTs a new project and returns
// a projectActionMsg with the result.
func (m *Model) createProjectCmd(req client.CreateProjectRequest) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
		defer cancel()
		p, err := m.c.CreateProject(ctx, req)
		return projectActionMsg{project: p, err: err}
	}
}

// pauseProjectCmd returns a tea.Cmd that pauses the selected project.
// Returns nil if no project is selected or the project is not active.
func (m *Model) pauseProjectCmd() tea.Cmd {
	id := m.selectedProjectIDForAction()
	if id == "" {
		return nil
	}
	state := m.actionProjectState(id)
	if state != stateActive {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
		defer cancel()
		p, err := m.c.PauseProject(ctx, id)
		return projectActionMsg{project: p, err: err}
	}
}

// resumeProjectCmd returns a tea.Cmd that resumes the selected project.
// Returns nil if no project is selected or the project is not paused.
func (m *Model) resumeProjectCmd() tea.Cmd {
	id := m.selectedProjectIDForAction()
	if id == "" {
		return nil
	}
	state := m.actionProjectState(id)
	if state != statePaused {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
		defer cancel()
		p, err := m.c.ResumeProject(ctx, id)
		return projectActionMsg{project: p, err: err}
	}
}

// finishProjectCmd returns a tea.Cmd that finishes the selected project.
// Returns nil if no project is selected or the project is already finished.
func (m *Model) finishProjectCmd() tea.Cmd {
	id := m.selectedProjectIDForAction()
	if id == "" {
		return nil
	}
	state := m.actionProjectState(id)
	if state == stateFinished || state == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
		defer cancel()
		p, err := m.c.FinishProject(ctx, id)
		return projectActionMsg{project: p, err: err}
	}
}

// selectedProjectIDForAction returns the ID of the project to act on: the
// drill-down selectedProjectID if set, otherwise the cursor-indexed project
// in the projects list. Returns "" if neither is available.
func (m *Model) selectedProjectIDForAction() string {
	if m.selectedProjectID != "" {
		return m.selectedProjectID
	}
	if i := m.selectedProjectIndex(); i >= 0 && i < len(m.projects) {
		return m.projects[i].ID
	}
	return ""
}

// actionProjectState returns the state of the project that a lifecycle
// command would act on, or "" if no project is found.
func (m *Model) actionProjectState(id string) string {
	for _, p := range m.projects {
		if p.ID == id {
			return p.State
		}
	}
	return ""
}

// centerDivisorForm halves free space to center the footer text. It mirrors
// centerDivisor from palette.go but is local to avoid cross-file coupling.
const centerDivisorForm = 2

// renderForm builds the new-project modal shown while the form is open. It
// is composited as its own layer over the dimmed background, so it uses
// styles directly rather than Model.paint.
func (m *Model) renderForm() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	faint := lipgloss.NewStyle().Faint(true)

	var b strings.Builder
	b.WriteString(titleStyle.Render("New project"))
	b.WriteString("\n\n")

	for i := 0; i < formFieldCount; i++ {
		label := formFieldLabels[i]
		value := m.form.fields[i]
		switch {
		case i == m.form.cursor:
			cursor := lipgloss.NewStyle().Reverse(true).Render(" ")
			fmt.Fprintf(&b, "  %-11s %s%s\n", label, value, cursor)
		case value == "":
			fmt.Fprintf(&b, "  %-11s %s\n", label, faint.Render("(empty)"))
		default:
			fmt.Fprintf(&b, "  %-11s %s\n", label, value)
		}
	}

	b.WriteString("\n")
	footer := "enter create · esc cancel"
	pad := (formInner - len(footer)) / centerDivisorForm
	if pad < 0 {
		pad = 0
	}
	b.WriteString(strings.Repeat(" ", pad))
	b.WriteString(faint.Render(footer))

	box := lipgloss.NewStyle().
		Width(formWidth).
		Padding(formPadY, formPadX).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
	return box.Render(b.String())
}

// handleProjectAction processes a projectActionMsg: on error it logs and
// refreshes; on success it updates the local project list (replacing the
// project if it exists, appending if new) and refreshes from the server.
func (m *Model) handleProjectAction(msg *projectActionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.actionErr = "project action: " + msg.err.Error()
		logrus.WithError(msg.err).Debug("tui: project action failed")
		return m, m.loadNode
	}
	m.actionErr = ""
	if msg.project.ID != "" {
		found := false
		for i := range m.projects {
			if m.projects[i].ID == msg.project.ID {
				m.projects[i] = msg.project
				found = true
				break
			}
		}
		if !found {
			m.projects = append(m.projects, msg.project)
		}
	}
	return m, m.loadNode
}
