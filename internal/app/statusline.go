package app

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// defaultSeparator is the glyph drawn between status-line blocks when the
// StatusLine has no explicit Separator set.
const defaultSeparator = ">"

// StatusBlock is one segment of the status line. Render returns the segment's
// text (already styled, via Model.paint so it dims with the palette overlay);
// returning "" omits the block and its separator entirely. Name identifies the
// block for StatusLine.Remove.
type StatusBlock struct {
	Name   string
	Render func(m *Model) string
}

// StatusLine is a configurable, right-aligned bottom bar composed of ordered
// blocks joined by a separator. Blocks can be added and removed at runtime.
type StatusLine struct {
	// Separator is the glyph drawn between blocks (default defaultSeparator).
	Separator string
	blocks    []StatusBlock
}

// NewStatusLine returns an empty status line using the default separator.
func NewStatusLine() *StatusLine {
	return &StatusLine{Separator: defaultSeparator}
}

// DefaultStatusLine returns the status line the TUI ships with: node
// connection state followed by the command-palette hint.
func DefaultStatusLine() *StatusLine {
	s := NewStatusLine()
	s.Add(nodeStatusBlock())
	s.Add(commandsBlock())
	return s
}

// Add appends a block to the right end of the status line.
func (s *StatusLine) Add(b StatusBlock) { s.blocks = append(s.blocks, b) }

// Remove drops the first block with the given name. It returns true if a block
// was removed.
func (s *StatusLine) Remove(name string) bool {
	for i, b := range s.blocks {
		if b.Name == name {
			s.blocks = append(s.blocks[:i], s.blocks[i+1:]...)
			return true
		}
	}
	return false
}

// Render joins the non-empty blocks with the separator and right-aligns the
// result within width. With width <= 0 (before the first WindowSizeMsg) the
// segments are returned without alignment padding.
func (s *StatusLine) Render(m *Model, width int) string {
	sep := s.Separator
	if sep == "" {
		sep = defaultSeparator
	}

	var segs []string
	for _, b := range s.blocks {
		if b.Render == nil {
			continue
		}
		if txt := b.Render(m); txt != "" {
			segs = append(segs, txt)
		}
	}
	if len(segs) == 0 {
		return ""
	}

	joiner := m.paint(lipgloss.NewStyle().Faint(true).Render, " "+sep+" ")
	content := strings.Join(segs, joiner)
	if width <= 0 {
		return content
	}
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Right).Render(content)
}

// nodeStatusBlock reports whether the TUI is connected to a node.
func nodeStatusBlock() StatusBlock {
	return StatusBlock{
		Name: "node",
		Render: func(m *Model) string {
			if m.connected {
				return m.paint(lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render, "connected")
			}
			return m.paint(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render, "disconnected")
		},
	}
}

// commandsBlock renders the "ctrl+p commands" hint, with the key chord in bold.
func commandsBlock() StatusBlock {
	return StatusBlock{
		Name: "commands",
		Render: func(m *Model) string {
			key := m.paint(lipgloss.NewStyle().Bold(true).Render, "ctrl+p")
			return key + m.paint(lipgloss.NewStyle().Render, " commands")
		},
	}
}
