package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// anyTab marks a palette entry that works wherever you are.
const anyTab tab = -1

// paletteEntry is one thing the palette can do. Running it presses its key
// on its tab, so the palette can never do anything the keyboard cannot, and
// the two never drift apart.
type paletteEntry struct {
	title string
	tab   tab
	key   string
	// show is the key as a person would read it, when that differs.
	show string
	// row is the Account section the key belongs to, selected before the
	// key is pressed.
	row string
	// run, when set, is what the entry does instead of pressing a key. It is
	// for the entries made on the spot, such as one for each saved view.
	run func(m *Model) tea.Cmd
}

func (e paletteEntry) keyLabel() string {
	if e.show != "" {
		return e.show
	}
	return e.key
}

// paletteEntries is everything the palette offers, most used first.
var paletteEntries = []paletteEntry{
	{title: "Todos: new todo", tab: tabTodos, key: "n"},
	{title: "Todos: quick capture, one per line", tab: tabTodos, key: "N"},
	{title: "Todos: complete or reopen", tab: tabTodos, key: "space"},
	{title: "Todos: edit", tab: tabTodos, key: "e"},
	{title: "Todos: reschedule to today", tab: tabTodos, key: "t"},
	{title: "Todos: reschedule to tomorrow", tab: tabTodos, key: "T"},
	{title: "Todos: reschedule to a week from today", tab: tabTodos, key: "w"},
	{title: "Todos: delete", tab: tabTodos, key: "d"},
	{title: "Todos: undo the last action", tab: tabTodos, key: "z"},
	{title: "Todos: mark for a bulk action", tab: tabTodos, key: "v"},
	{title: "Todos: move the marked todos to a list", tab: tabTodos, key: "m"},
	{title: "Todos: move a day later", tab: tabTodos, key: ">"},
	{title: "Todos: move a day earlier", tab: tabTodos, key: "<"},
	{title: "Todos: board, a column per day", tab: tabTodos, key: "b"},
	{title: "Todos: show the next list", tab: tabTodos, key: "]"},
	{title: "Todos: show the previous list", tab: tabTodos, key: "["},
	{title: "Todos: show or hide the sidebar", tab: tabTodos, key: "\\"},
	{title: "Todos: search", tab: tabTodos, key: "/"},
	{title: "Todos: change the view (everything, overdue, today…)", tab: tabTodos, key: "f"},
	{title: "Todos: show one list", tab: tabTodos, key: "F"},
	{title: "Todos: take the todo, or hand yours back to Anyone (shared lists)", tab: tabTodos, key: "i"},
	{title: "Todos: cycle assignee filter", tab: tabTodos, key: "a"},
	{title: "Todos: show one person's todos", tab: tabTodos, key: "@"},
	{title: "Todos: only undated todos, the ones with no date", tab: tabTodos, key: "U"},
	{title: "Views: save the filters on screen", tab: tabTodos, key: "s"},
	{title: "Views: open, rename, update or remove a saved view", tab: tabTodos, key: "S"},
	{title: "Todos: jump to the inbox", tab: tabTodos, key: "u"},
	{title: "Todos: preview how a title parses", tab: tabTodos, key: "p"},
	{title: "Todos: load more", tab: tabTodos, key: "L"},
	{title: "Todos: copy the title", tab: tabTodos, key: "y"},
	{title: "Todos: copy the id", tab: tabTodos, key: "Y"},
	{title: "Lists: new list", tab: tabLists, key: "n"},
	{title: "Lists: rename", tab: tabLists, key: "e"},
	{title: "Lists: delete or leave", tab: tabLists, key: "d"},
	{title: "Lists: show members", tab: tabLists, key: "m"},
	{title: "Lists: invite someone by username or email", tab: tabLists, key: "a"},
	{title: "Lists: create a 7-day invite link", tab: tabLists, key: "i"},
	{title: "Lists: invitations waiting for you", tab: tabLists, key: "I"},
	{title: "Lists: join with an invite", tab: tabLists, key: "J"},
	{title: "Lists: show this list in Todos", tab: tabLists, key: "t"},
	{title: "Sync: pull changes since the cursor", tab: tabSync, key: "p"},
	{title: "Sync: full pull", tab: tabSync, key: "f"},
	{title: "Account: set your username", tab: tabAccount, key: "e", row: "profile"},
	{title: "Account: remove your username", tab: tabAccount, key: "x", row: "profile"},
	{title: "Account: sign out of this computer", tab: tabAccount, key: "o", row: "signout"},
	{title: "Account: change the saved token", tab: tabAccount, key: "c", row: "computer"},
	{title: "Account: copy the settings path", tab: tabAccount, key: "y", row: "computer"},
	{title: "Go to Todos", tab: anyTab, key: "1"},
	{title: "Go to Lists", tab: anyTab, key: "2"},
	{title: "Go to Sync", tab: anyTab, key: "3"},
	{title: "Go to Account", tab: anyTab, key: "4"},
	{title: "Choose a theme", tab: anyTab, key: "ctrl+t"},
	{title: "Refresh everything from the server", tab: anyTab, key: "r"},
	{title: "Show all keys", tab: anyTab, key: "?"},
	{title: "Quit", tab: anyTab, key: "q"},
}

// viewEntries are the palette's entries for the saved views, made each time
// the palette opens so they are the views of whoever is signed in.
func (m *Model) viewEntries() []paletteEntry {
	var out []paletteEntry
	for _, sv := range m.todos.savedViews() {
		name := sv.Name
		out = append(out, paletteEntry{
			title: "Views: open " + name, tab: tabTodos, show: "view",
			run: func(m *Model) tea.Cmd { return m.todos.openSaved(m, name) },
		})
	}
	return out
}

// palette is the searchable list of actions.
type palette struct {
	entries  []paletteEntry
	input    textinput.Model
	matches  []int
	cursor   int
	width    int
	visibleN int
}

func newPalette(width int, extra []paletteEntry) *palette {
	ti := newInput()
	ti.Prompt = "❯ "
	ti.PromptStyle = styAccent
	ti.Placeholder = "type to find an action"
	ti.CharLimit = 80
	ti.Focus()
	p := &palette{
		entries: append(append([]paletteEntry{}, paletteEntries...), extra...),
		input:   ti, width: paletteWidth(width), visibleN: 12,
	}
	p.filter()
	return p
}

// paletteWidth is how wide the palette is drawn on a screen this wide: a
// margin either side where there is room, and never wider than the screen,
// which would cut off the keys on the right.
func paletteWidth(screen int) int {
	return max(min(screen-8, 72), min(screen, 30))
}

// filter keeps the entries containing every word typed, in list order.
func (p *palette) filter() {
	words := strings.Fields(strings.ToLower(p.input.Value()))
	p.matches = p.matches[:0]
	for i, e := range p.entries {
		hay := strings.ToLower(e.title + " " + e.keyLabel())
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			p.matches = append(p.matches, i)
		}
	}
	p.cursor = 0
}

func (p *palette) selected() (paletteEntry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.matches) {
		return paletteEntry{}, false
	}
	return p.entries[p.matches[p.cursor]], true
}

func (p *palette) view() string {
	inner := p.width - 4
	p.input.Width = max(inner-4, 10)
	lines := []string{brandGradient("Command palette"), "", p.input.View(), ""}
	if len(p.matches) == 0 {
		lines = append(lines, styFaint.Render("nothing matches"))
	}
	start := 0
	if p.cursor >= p.visibleN {
		start = p.cursor - p.visibleN + 1
	}
	for i := start; i < len(p.matches) && i < start+p.visibleN; i++ {
		e := p.entries[p.matches[i]]
		sel := i == p.cursor
		base := lipgloss.NewStyle()
		bar := " "
		if sel {
			base = base.Background(colPanel)
			bar = base.Foreground(colAccent).Render("▌")
		}
		// The part before the colon says where the action lives, so it is
		// set back and the action itself carries the row.
		group, action, ok := strings.Cut(e.title, ": ")
		if !ok {
			group, action = "", e.title
		}
		text := base.Foreground(colFg)
		if sel {
			text = text.Bold(true)
		}
		left := base.Foreground(colFaint).Render(padTo(group, 8)) + text.Render(action)
		key := base.Foreground(colAccent).Bold(true).Render(e.keyLabel())
		gap := max(inner-1-visibleWidth(left)-visibleWidth(key)-1, 1)
		if gap == 1 {
			left = truncate(left, max(inner-3-visibleWidth(key), 4))
			gap = max(inner-1-visibleWidth(left)-visibleWidth(key)-1, 1)
		}
		lines = append(lines, bar+left+base.Render(strings.Repeat(" ", gap))+key+base.Render(" "))
	}
	if extra := len(p.matches) - (start + p.visibleN); extra > 0 {
		lines = append(lines, styFaint.Render(fmt.Sprintf(" %d more, keep typing to narrow", extra)))
	}
	lines = append(lines, "", styFaint.Render("↑↓ choose · enter run · esc close"))
	body := lipgloss.NewStyle().Width(inner).Render(strings.Join(lines, "\n"))
	return styBox.Render(body)
}

// openPalette shows the palette.
func (m *Model) openPalette() tea.Cmd {
	m.palette = newPalette(m.width, m.viewEntries())
	m.layer = layerPalette
	return textinput.Blink
}

func (m *Model) onPaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	if p == nil {
		m.layer = layerNone
		return m, nil
	}
	switch msg.String() {
	case "esc", "ctrl+p", "ctrl+g":
		m.layer = layerNone
		m.palette = nil
		return m, nil
	case "up", "ctrl+k":
		p.cursor = max(p.cursor-1, 0)
		return m, nil
	case "down", "ctrl+j":
		p.cursor = min(p.cursor+1, max(len(p.matches)-1, 0))
		return m, nil
	case "enter":
		e, ok := p.selected()
		if !ok {
			return m, nil
		}
		m.layer = layerNone
		m.palette = nil
		return m, m.runPalette(e)
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	p.filter()
	return m, cmd
}

// runPalette does what the entry's key would do, on the entry's tab.
func (m *Model) runPalette(e paletteEntry) tea.Cmd {
	var cmds []tea.Cmd
	if e.tab != anyTab {
		if e.tab != m.tab {
			cmds = append(cmds, m.switchTab(e.tab))
		}
		m.focus = focusList
	}
	if e.row != "" {
		m.acct.selectRow(e.row)
	}
	if e.run != nil {
		return tea.Batch(append(cmds, e.run(m))...)
	}
	km, ok := parseKey(e.key)
	if !ok {
		return m.warnf("the palette has no key for %q", e.title)
	}
	_, c := m.dispatchKey(km)
	return tea.Batch(append(cmds, c)...)
}
