package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// -- layout -----------------------------------------------------------------

// The screen is a header row, the active view, and a footer row.
const (
	headerRows = 1
	footerRows = 1
)

func (m *Model) resize() {
	h := m.bodyHeight()
	m.todos.setViewport(m.width, h)
	m.listsV.setViewport(m.width, h)
	m.syncV.setViewport(m.width, h)
	// A pane the terminal no longer has room for must not keep the
	// keyboard, or keys would move and pick things that cannot be seen.
	switch {
	case m.focus == focusSidebar && m.todos.layout(m.width, h).sideW == 0,
		m.focus == focusMembers && !m.listsV.membersInView(m.width, h):
		m.focus = focusList
	}
	if m.help != nil {
		m.help.width = max(m.width-8, 20)
		m.help.height = max(m.height-8, 3)
	}
	if m.form != nil {
		m.form.width = min(m.width-8, 78)
		m.form.maxH = max(m.height-6, 10)
	}
}

func (m *Model) bodyHeight() int {
	rows := headerRows + footerRows
	if m.cfg.Demo {
		rows++
	}
	return max(m.height-rows, 1)
}

func (m *Model) View() string {
	if m.width == 0 {
		return "starting evado…"
	}
	if m.quited {
		return ""
	}
	if m.layer == layerLogin {
		return m.login.view(m.width, m.height)
	}
	if !m.ready && m.busy && m.user.ID == "" {
		return m.centered(m.splashView())
	}

	var body string
	switch m.tab {
	case tabTodos:
		body = m.todos.view(m.width, m.bodyHeight())
	case tabLists:
		body = m.listsV.view(m.width, m.bodyHeight())
	case tabSync:
		body = m.syncV.view(m.width, m.bodyHeight())
	case tabAccount:
		body = m.acct.view(m.width, m.bodyHeight())
	}
	top := m.headerView()
	if m.cfg.Demo {
		top += "\n" + styAccent.Render(" DEMO · changes discarded on exit")
	}
	screen := top + "\n" + clipBlock(body, m.width, m.bodyHeight()) + "\n" + m.footerView()

	// The screen is padded to the full terminal height before anything is
	// drawn over it, so a dialog is never clipped by a short body.
	screen = fitScreen(screen, m.width, m.height)
	return m.withOverlay(m.withToast(screen))
}

// fitScreen clips a composed screen to the terminal size and pads it out,
// so the frame always has exactly height lines.
func fitScreen(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, l := range lines {
		if visibleWidth(l) > width {
			lines[i] = lipgloss.NewStyle().MaxWidth(width).Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// withToast floats the status message over the bottom right corner, so a
// message never moves the view it is about.
func (m *Model) withToast(screen string) string {
	if m.toast == nil || m.width < 12 || m.height < 5 {
		return screen
	}
	t := m.toast
	text := t.style().Bold(true).Render(t.prefix()) + lipgloss.NewStyle().Foreground(colFg).Render(truncate(t.text, max(m.width-8, 4)))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.color()).
		Padding(0, 1).
		Render(text)
	w := lipgloss.Width(box)
	return overlayAt(screen, box, max(m.width-w-1, 0), max(m.height-footerRows-lipgloss.Height(box), 0), w)
}

// withOverlay draws the active modal on top of the finished screen, so a
// form or a confirmation always covers whatever it interrupts. The screen
// behind is dimmed, which is what makes the dialog read as being in front.
func (m *Model) withOverlay(screen string) string {
	var modal string
	switch m.layer {
	case layerForm:
		modal = m.form.view()
	case layerConfirm:
		modal = m.conf.view()
	case layerPalette:
		modal = m.palette.view()
	case layerThemes:
		modal = m.themes.view()
	case layerHelp:
		modal = styBox.Render(m.help.view())
	}
	if modal == "" {
		return screen
	}
	return overlay(dim(screen), modal, m.width, m.height)
}

// dim redraws a screen in the faintest colour.
func dim(screen string) string {
	if plain() {
		return screen
	}
	lines := strings.Split(screen, "\n")
	for i, l := range lines {
		lines[i] = styFaint.Render(ansi.Strip(l))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) centered(s string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, s)
}

func (m *Model) splashView() string {
	parts := []string{}
	if m.height >= catchyRows+8 && m.width >= 40 {
		parts = append(parts, renderCatchy(), "")
	}
	status := m.busyLabel
	if status == "" {
		status = "loading your todos"
	}
	parts = append(parts,
		brandGradient("e v a d o"),
		"",
		styAccent.Render(m.spinner())+" "+styMuted.Render(status),
		styFaint.Render(m.serverURL()),
	)
	return lipgloss.JoinVertical(lipgloss.Center, parts...)
}

// spinner is the current frame of the busy indicator.
func (m *Model) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

// brandMark is the wordmark at the start of the header.
const brandMark = " ◆ evado "

// tabX is the column a tab's label starts at, for drawing and for clicks.
func tabX(t tab) int {
	x := visibleWidth(brandMark) + 1
	for i := 0; i < int(t); i++ {
		x += len(tabNames[i]) + 3
	}
	return x
}

func (m *Model) headerView() string {
	var b strings.Builder
	b.WriteString(brandGradient(brandMark) + " ")
	for i, name := range tabNames {
		if tab(i) == m.tab {
			b.WriteString(styPill.Render(" " + name + " "))
		} else {
			b.WriteString(styMuted.Render(" " + name + " "))
		}
		b.WriteString(" ")
	}
	left := b.String()

	// The right side gives way before the tabs do: first the progress
	// meter, then the counts.
	busy := ""
	if m.busy {
		busy = styAccent.Render(m.spinner())
		if m.busyLabel != "" {
			busy += " " + styMuted.Render(m.busyLabel)
		}
	}
	chip := m.syncChip()
	for _, right := range []string{
		joinNonEmpty("  ", busy, chip, m.progressView(), m.countsView()),
		joinNonEmpty("  ", busy, chip, m.countsView()),
		joinNonEmpty("  ", busy, chip),
		busy,
	} {
		if visibleWidth(left)+visibleWidth(right)+2 <= m.width {
			return hstack(left, right+" ", m.width)
		}
	}
	return padTo(left, m.width)
}

func joinNonEmpty(sep string, parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}

// progressView is how far through today the account is: what was finished
// today against what is still due today or overdue.
func (m *Model) progressView() string {
	s := m.todos.stats()
	total := s.doneToday + s.dueToday + s.overdue
	if total == 0 {
		return ""
	}
	return meter(s.doneToday, total, 10) + " " + styMuted.Render(fmt.Sprintf("%d/%d", s.doneToday, total))
}

// countsView summarises the whole account, not the current view. It counts
// with the same rules that place todos in sections, so the header and the
// Overdue and Today headings can never disagree, and it reads the last
// unfiltered load so narrowing the list does not change it.
func (m *Model) countsView() string {
	if m.todos.snapshot == nil {
		return ""
	}
	s := m.todos.stats()
	if s.open == 0 {
		return styOK.Render("✓ all clear")
	}
	parts := []string{styMuted.Render(fmt.Sprintf("%d open", s.open))}
	if s.dueToday > 0 {
		parts = append(parts, stySoon.Render(fmt.Sprintf("%d today", s.dueToday)))
	}
	if s.overdue > 0 {
		parts = append(parts, styOver.Render(fmt.Sprintf("%d overdue", s.overdue)))
	}
	return strings.Join(parts, styFaint.Render(" · "))
}

func (m *Model) footerView() string {
	if m.layer == layerSearch {
		return hstack(" "+m.search.View(), styFaint.Render("enter search the server · esc clear "), m.width)
	}
	var hints []helpEntry
	switch m.tab {
	case tabTodos:
		hints = m.todos.footer()
	case tabLists:
		hints = m.listsV.footer()
	case tabSync:
		hints = m.syncV.footer()
	case tabAccount:
		hints = m.acct.footer()
	}
	extra := styKey.Render(":") + styFaint.Render(" commands  ") + styKey.Render("?") + styFaint.Render(" help ")
	return hstack(" "+hintLine(hints, max(m.width-visibleWidth(extra)-3, 0)), extra, m.width)
}

// hintLine lays key hints out in a row, dropping the ones that do not fit
// from the end rather than cutting one in half.
func hintLine(hints []helpEntry, width int) string {
	var b strings.Builder
	used := 0
	for _, h := range hints {
		part := styKey.Render(h.keys) + " " + styFaint.Render(h.desc)
		w := visibleWidth(part)
		if used > 0 {
			w += 2
		}
		if used+w > width {
			break
		}
		if used > 0 {
			b.WriteString("  ")
		}
		b.WriteString(part)
		used += w
	}
	return b.String()
}
