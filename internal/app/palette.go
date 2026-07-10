package app

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	// paletteWidth is the width of the command dialog's content box (the
	// border adds two more columns).
	paletteWidth = 44
	// palettePadX / palettePadY are the dialog's horizontal / vertical inner
	// padding.
	palettePadX = 2
	palettePadY = 1
	// paletteInner is the usable content width inside the horizontal padding.
	paletteInner = paletteWidth - palettePadX*2
	// centerDivisor halves free space to center the dialog on each axis.
	centerDivisor = 2
)

// paletteItem is one selectable command shown in the palette, with its
// label and the key chord that triggers it.
type paletteItem struct {
	label string
	key   string
}

// paletteItems returns the commands available in the current state. Refresh is
// offered when connected; Retry when waiting to reconnect.
func (m *Model) paletteItems() []paletteItem {
	var items []paletteItem
	if m.connected {
		items = append(items, paletteItem{label: "Refresh", key: "r"})
	} else {
		items = append(items, paletteItem{label: "Retry now", key: "r"})
	}
	items = append(items, paletteItem{label: "Quit", key: "q"})
	return items
}

// renderPalette builds the command dialog shown while paletteOpen is true. It
// is composited as its own layer over the dimmed background, so it uses styles
// directly rather than Model.paint.
func (m *Model) renderPalette() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	faint := lipgloss.NewStyle().Faint(true)

	var b strings.Builder
	b.WriteString(spread(paletteInner, titleStyle.Render("Commands"), faint.Render("esc")))
	b.WriteString("\n\n")

	items := m.paletteItems()
	for i, it := range items {
		b.WriteString(spread(paletteInner, it.label, faint.Render(it.key)))
		if i < len(items)-1 {
			b.WriteString("\n")
		}
	}

	box := lipgloss.NewStyle().
		Width(paletteWidth).
		Padding(palettePadY, palettePadX).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("252"))
	return box.Render(b.String())
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

// paletteOffset centers the dialog within the current terminal, clamped so it
// never renders off the top-left edge.
func (m *Model) paletteOffset(dialog string) (x, y int) {
	x = (m.width - lipgloss.Width(dialog)) / centerDivisor
	y = (m.height - lipgloss.Height(dialog)) / centerDivisor
	return max(x, 0), max(y, 0)
}
