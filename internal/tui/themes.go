package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/config"
)

// themePicker chooses a theme. Moving the cursor applies the theme at
// once, so the choice is made looking at the real screen rather than at a
// name.
type themePicker struct {
	cursor int
	// orig is the theme to go back to if the picker is cancelled.
	orig string
}

// autoTheme is the picker's entry for following the terminal.
const autoTheme = "auto"

// themeChoices is what the picker lists: the terminal's own background
// first, then every built-in theme.
func themeChoices() []string {
	names := []string{autoTheme}
	for _, s := range schemes {
		names = append(names, s.name)
	}
	return names
}

func (m *Model) openThemes() {
	p := &themePicker{orig: m.cfg.Theme}
	for i, name := range themeChoices() {
		if name == current.name && m.cfg.Theme != "" && m.cfg.Theme != autoTheme {
			p.cursor = i
		}
	}
	m.themes = p
	m.layer = layerThemes
}

// setTheme applies a theme and redraws what caches its colours.
func (m *Model) setTheme(name string) {
	applyTheme(name)
	m.todos.rebuild(m.todos.selectedID())
}

func (m *Model) onThemeKey(msg tea.KeyMsg) tea.Cmd {
	p := m.themes
	names := themeChoices()
	switch msg.String() {
	case "esc", "q":
		m.setTheme(p.orig)
		m.layer, m.themes = layerNone, nil
		return nil
	case "up", "k":
		p.cursor = (p.cursor - 1 + len(names)) % len(names)
	case "down", "j", "tab":
		p.cursor = (p.cursor + 1) % len(names)
	case "enter":
		name := names[p.cursor]
		m.cfg.Theme = name
		m.layer, m.themes = layerNone, nil
		if err := config.SaveTheme(name); err != nil {
			return m.warnf("theme applied, but not saved: %s", errText(err))
		}
		return m.okf("theme saved")
	default:
		return nil
	}
	m.setTheme(names[p.cursor])
	return nil
}

func (p *themePicker) pick(press func(string) tea.Cmd, i int, double bool) tea.Cmd {
	if double {
		return press("enter")
	}
	n := len(themeChoices())
	for steps := (i - p.cursor + n) % n; steps > 0; steps-- {
		press("down")
	}
	return nil
}

func (p *themePicker) view() string {
	press := pressing()
	lines := []string{styTitle.Render("Theme"), ""}
	for i, name := range themeChoices() {
		title := "Match the terminal"
		var swatch string
		if s, ok := schemeByName(name); ok {
			title = s.label
			for _, c := range []string{s.accent, s.accent2, s.green, s.blue, s.amber, s.red} {
				swatch += lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("●") + " "
			}
		}
		row := " " + padTo(title, 20) + " " + padTo(swatch, 12)
		if i == p.cursor {
			row = styAccent.Render("▌") + stySel.Render(padTo(title, 20)+" ") + padTo(swatch, 12)
		}
		lines = append(lines, clickable(row, func(double bool) tea.Cmd { return p.pick(press, i, double) }))
	}
	lines = append(lines, "", styFaint.Render("↑↓ try one · enter keep · esc cancel"))
	return scrollable(styBox.Render(strings.Join(lines, "\n")), func(delta int) tea.Cmd {
		if delta < 0 {
			return press("up")
		}
		return press("down")
	})
}
