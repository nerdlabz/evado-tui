package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// syncView shows the change feed: every todo changed since a cursor,
// tombstones included. It is the fastest way to see what other devices
// did, and the same cursor drives a full /sync pull.
type syncView struct {
	m *Model

	rows table
	feed []api.SyncTodo

	cursor     string
	hasMore    bool
	pulling    bool
	entered    bool
	lastPulled time.Time
	pulls      int
	errs       []api.SyncError
}

func newSyncView(m *Model) syncView { return syncView{m: m} }

func (v *syncView) setViewport(_, h int) { v.rows.setHeight(max(h-2, 1)) }

// onEnter runs an initial full pull the first time the tab is opened, so
// there is something to look at without pressing anything.
func (v *syncView) onEnter(m *Model) tea.Cmd {
	if v.entered {
		return nil
	}
	v.entered = true
	if v.cursor != "" {
		return nil
	}
	v.pulling = true
	m.busy = true
	return tea.Batch(m.setBusy("initial sync"), pullChanges(m.ctx, m.client, ""))
}

func (v *syncView) load() tea.Cmd {
	if v.cursor == "" {
		return nil
	}
	v.pulling = true
	return pullChanges(v.m.ctx, v.m.client, v.cursor)
}

func (v *syncView) onKey(m *Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "up", "k":
		v.rows.move(-1)
		return m, nil
	case "down", "j":
		v.rows.move(1)
		return m, nil
	case "pgup", "ctrl+b":
		v.rows.pageUp()
		return m, nil
	case "pgdown", "ctrl+f":
		v.rows.pageDown()
		return m, nil
	case "g", "home":
		v.rows.top()
		return m, nil
	case "G", "end":
		v.rows.bottom()
		return m, nil
	case "enter":
		// Jump to the todo in the Todos tab.
		i := v.rows.selected()
		if i < 0 || i >= len(v.feed) {
			return m, nil
		}
		id := v.feed[i].ID
		m.tab = tabTodos
		m.focus = focusList
		m.busy = true
		return m, tea.Batch(m.setBusy(""), m.todos.load(), m.openTodoByID(id))
	case "p":
		return m, v.pull(m)
	case "f":
		return m, v.fullPull(m)
	case "c":
		v.cursor = ""
		v.feed = nil
		v.errs = nil
		v.rows.setItems(0)
		return m, m.okf("cursor cleared; press f for a full pull")
	case "d":
		i := v.rows.selected()
		if i < 0 || i >= len(v.feed) {
			return m, nil
		}
		t := v.feed[i]
		if t.Tombstoned() {
			return m, m.warnf("that todo is already deleted")
		}
		todo := t.Todo
		return m, m.ask("delete-todo", "Delete this todo?",
			fmt.Sprintf("%q was changed elsewhere; deleting it now syncs the deletion back.", todo.Title), false,
			func() tea.Cmd { return m.mutate(todo.ID, deleteTodo(m.ctx, m.client, todo)) })
	}
	return m, nil
}

func (v *syncView) pull(m *Model) tea.Cmd {
	if v.pulling {
		return nil
	}
	v.pulling = true
	m.busy = true
	return tea.Batch(m.setBusy("pulling changes"), pullChanges(m.ctx, m.client, v.cursor))
}

func (v *syncView) fullPull(m *Model) tea.Cmd {
	v.pulling = true
	m.busy = true
	return tea.Batch(m.setBusy("full pull"), pullChanges(m.ctx, m.client, ""))
}

func (v *syncView) onResult(msg syncMsg) tea.Cmd {
	m := v.m
	v.pulling = false
	m.done()
	if msg.err != nil {
		return m.failf(msg.err, "sync")
	}
	resp := msg.resp
	v.feed = resp.Todos
	v.rows.setItems(len(v.feed))
	v.rows.top()
	v.hasMore = resp.HasMore
	// Keep the cursor the service gave, so the next pull continues from it.
	if resp.Cursor != "" {
		v.cursor = resp.Cursor
	}
	v.lastPulled = time.Now()
	v.pulls++
	if len(resp.Errors) > 0 {
		v.errs = resp.Errors
	}
	if msg.resp.HasMore {
		// Drain the feed straight away, as the spec asks.
		return v.pull(m)
	}
	return m.okf("%d change(s) in the feed", len(v.feed))
}

// status says where the feed stands, for the pane's title.
func (v *syncView) status() string {
	parts := []string{"Change feed"}
	if v.cursor == "" {
		parts = append(parts, "no cursor: this pull returned everything")
	} else {
		parts = append(parts, "last pulled "+relative(&v.lastPulled, v.m.now))
	}
	if v.hasMore {
		parts = append(parts, "more changes pending")
	}
	return strings.Join(parts, " · ")
}

func (v *syncView) view(width, height int) string {
	w, h := width, height
	if framed(width, height) {
		w, h = width-2, height-2
	}
	var b strings.Builder
	errRows := 0
	if len(v.errs) > 0 {
		errRows = len(v.errs) + 2
	}
	v.rows.setHeight(max(h-errRows, 1))
	if len(v.feed) == 0 {
		msg := "Nothing has changed since the cursor."
		if v.lastPulled.IsZero() {
			msg = "No changes pulled yet. Press f for a full pull, p to fetch since the cursor."
		}
		b.WriteString("\n " + styMuted.Render(truncate(msg, max(w-2, 4))))
	} else {
		b.WriteString(v.rows.render(w, func(i int, sel bool) string {
			return v.row(i, sel, w-1)
		}, v.clickChange))
	}
	if len(v.errs) > 0 {
		b.WriteString("\n\n " + styErr.Render("per-change errors"))
		for _, e := range v.errs {
			b.WriteString("\n   " + styFaint.Render(e.ID+": "+e.Error))
		}
	}
	if !framed(width, height) {
		return scrollable(b.String(), v.m.wheelKeys)
	}
	return scrollable(frame(b.String(), width, height, frameOpts{
		title:   v.status(),
		foot:    styMuted.Render(plural(len(v.feed), "change")),
		focused: true,
	}), v.m.wheelKeys)
}

func (v *syncView) clickChange(i int, double bool) tea.Cmd {
	v.rows.cursor = i
	v.rows.clamp()
	if double {
		return v.m.pressKey("enter")
	}
	return nil
}

func (v *syncView) row(i int, sel bool, width int) string {
	m := v.m
	t := v.feed[i]
	base := lipgloss.NewStyle()
	bar := " "
	if sel {
		base = base.Background(colPanel)
		bar = base.Foreground(colAccent).Render("▌")
	}
	// A tombstone outranks a completion: the row is about the deletion.
	mark := base.Foreground(colMuted).Render("○")
	text := base.Foreground(colFg)
	kind, kindSty := "edited", base.Foreground(colMuted)
	switch {
	case t.Tombstoned():
		mark = base.Foreground(colRed).Render("×")
		text = base.Foreground(colFaint).Strikethrough(true)
		kind, kindSty = "deleted", base.Foreground(colRed)
	case t.Completed():
		mark = base.Foreground(colGreen).Render("✓")
		text = base.Foreground(colFaint).Strikethrough(true)
		if t.CompletedAt != nil && t.UpdatedAt.Sub(*t.CompletedAt) < time.Minute {
			kind, kindSty = "completed", base.Foreground(colGreen)
		}
	}
	if n := len(t.Checklist); n > 0 {
		kind += fmt.Sprintf(" [%d]", n)
	}
	when := relative(&t.UpdatedAt, m.now)
	// The mark and its space take three cells; when and kind are fixed.
	leftW := max(width-3-15-14, 8)
	title := truncate(t.Title, leftW)
	row := bar + mark + base.Render(" ") + text.Render(title) +
		base.Render(strings.Repeat(" ", max(leftW-visibleWidth(title), 0)+1)) +
		base.Foreground(colFaint).Render(padTo(when, 14)) + base.Render(" ") + kindSty.Render(kind)
	return row + base.Render(strings.Repeat(" ", max(width-visibleWidth(row), 0)))
}

func (v *syncView) footer() []helpEntry {
	return []helpEntry{
		{"p", "pull changes"},
		{"f", "full pull"},
		{"c", "clear cursor"},
		{"j/k", "move"},
		{"enter", "open in todos"},
		{"d", "delete"},
	}
}

// openTodoByID switches the Todos tab and loads a specific todo, which is
// fetched directly when it is not in the current filter.
func (m *Model) openTodoByID(id string) tea.Cmd {
	m.todos.reveal(id)
	return m.todos.openDetail(m, id)
}
