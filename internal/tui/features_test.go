package tui

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/dates"
)

func findTodo(f *fakeAPI, id string) *api.Todo {
	for i := range f.todos {
		if f.todos[i].ID == id {
			return &f.todos[i]
		}
	}
	return nil
}

func TestStaleLoadIsIgnored(t *testing.T) {
	h := newHarness(t, sampleAPI())
	before := len(h.model.todos.items)
	if before == 0 {
		t.Fatal("expected todos to be loaded")
	}
	// A reply to an earlier load arrives after a newer one was asked for.
	h.model.todos.seq += 2
	h.send(todosMsg{todos: nil, seq: h.model.todos.seq - 1})
	if got := len(h.model.todos.items); got != before {
		t.Errorf("a stale reply replaced the list: %d items, want %d", got, before)
	}
	h.send(todosMsg{todos: nil, seq: h.model.todos.seq})
	if got := len(h.model.todos.items); got != 0 {
		t.Errorf("the current reply was ignored: %d items, want 0", got)
	}
}

func TestCompleteShowsImmediatelyAndRollsBackOnFailure(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g")
	id := h.model.todos.selected().ID

	cmd := h.model.todos.toggle(h.model) // not run yet: the server has not answered
	if cmd == nil {
		t.Fatal("toggle returned no command")
	}
	if !h.model.todos.selected().Completed() && !h.model.todos.shown(id) {
		t.Fatal("lost the todo")
	}
	var shownDone bool
	for _, todo := range h.model.todos.items {
		if todo.ID == id {
			shownDone = todo.Completed()
		}
	}
	if !shownDone {
		t.Error("the list should show the completion before the server replies")
	}

	// The server refuses, so the list has to go back to what is true.
	next, reload := h.model.Update(todoMsg{err: &api.Error{StatusCode: http.StatusInternalServerError, Code: "boom", Message: "boom"}})
	h.model = next.(*Model)
	h.run(reload)
	for _, todo := range h.model.todos.items {
		if todo.ID == id && todo.Completed() {
			t.Error("the failed completion was not rolled back")
		}
	}
}

func TestUndoCompletion(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "space")
	if !findTodo(f, "t1").IsCompleted {
		t.Fatal("space should complete the first todo")
	}
	h.press("z")
	if findTodo(f, "t1").IsCompleted {
		t.Error("z should reopen it")
	}
	h.mustContain("undone")
	h.press("z")
	h.mustContain("nothing to undo")
}

func TestUndoDeleteRecreatesTheTodo(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "d", "enter")
	if findTodo(f, "t1") != nil {
		t.Fatal("the todo should be deleted")
	}
	h.press("z")
	var restored bool
	for _, todo := range f.todos {
		if todo.Title == "Pay the cable bill" {
			restored = true
		}
	}
	if !restored {
		t.Error("z should bring the deleted todo back")
	}
}

// Deleting a todo leaves the cursor where it was, on the todo that took its
// place, both when the answer is merged in and when the list is read again.
func TestDeleteKeepsTheCursorsPlace(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{"every list", nil},
		{"one list", []string{"h", "G", "k", "enter"}}, // Household
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sampleAPI()
			past := time.Now().Add(-48 * time.Hour)
			for _, id := range []string{"t4", "t5", "t6"} {
				f.todos = append(f.todos, api.Todo{ID: id, ListID: "l2", Title: "Chore " + id, UpdatedAt: past, CreatedAt: past})
			}
			h := newHarness(t, f)
			h.press(tc.keys...)

			// The second inbox todo, so there is one above it and one below.
			v := &h.model.todos
			var inbox []string
			for _, l := range v.lines {
				if !l.header && v.sectionOf(v.items[l.index]) == "inbox" {
					inbox = append(inbox, v.items[l.index].ID)
				}
			}
			if len(inbox) < 3 {
				t.Fatalf("inbox = %v, want at least three todos", inbox)
			}
			v.reveal(inbox[1])
			h.press("d", "y")
			if findTodo(f, inbox[1]) != nil {
				t.Fatalf("%s was not deleted", inbox[1])
			}
			if got := h.model.todos.selectedID(); got != inbox[2] {
				t.Errorf("cursor on %q after the delete, want %q, the todo below", got, inbox[2])
			}

			// The last one has nothing below it, so the cursor goes up.
			var shown []string
			for _, l := range h.model.todos.lines {
				if !l.header {
					shown = append(shown, h.model.todos.items[l.index].ID)
				}
			}
			h.press("G", "d", "y")
			if got, want := h.model.todos.selectedID(), shown[len(shown)-2]; got != want {
				t.Errorf("cursor on %q after deleting the last todo, want %q above it", got, want)
			}
		})
	}
}

func TestMarkAndCompleteSeveral(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "v", "v")
	if got := len(h.model.todos.marks); got != 2 {
		t.Fatalf("marks = %d, want 2", got)
	}
	h.mustContain("2 marked")
	h.press("space")
	if !findTodo(f, "t1").IsCompleted || !findTodo(f, "t2").IsCompleted {
		t.Error("space should complete every marked todo")
	}
	if len(h.model.todos.marks) != 0 {
		t.Error("marks should clear after the action")
	}
}

func TestMarkAndDeleteSeveral(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "v", "v", "d")
	h.mustContain("Delete 2 todos?")
	h.press("enter")
	if len(f.deletes) != 2 {
		t.Errorf("deletes = %v, want two", f.deletes)
	}
}

func TestEscClearsMarks(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("g", "v")
	h.press("esc")
	if len(h.model.todos.marks) != 0 {
		t.Error("esc should clear the marks")
	}
	if h.model.focus != focusList {
		t.Error("esc should stay on the list")
	}
}

func TestSearchNarrowsAsYouType(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("/", "m", "i", "l", "k")
	h.mustContain("Buy milk")
	h.mustNotContain("Renew passport")
	h.mustContain("search: milk") // the box is visible while typing
	h.press("esc")
	h.mustContain("Renew passport")
}

func TestHeaderCountsIgnoreTheView(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.mustContain("2 open")
	h.mustContain("1 overdue")
	h.press("f", "f", "f", "f") // down to "Completed only"
	if h.model.todos.filter.scopeLabel() != "Completed only" {
		t.Fatalf("scope = %q", h.model.todos.filter.scopeLabel())
	}
	h.mustContain("2 open")
	h.mustContain("1 overdue")
}

func TestSlashFromAnotherTabOpensTodoSearch(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("2")
	if h.model.tab != tabLists {
		t.Fatalf("tab = %v, want lists", h.model.tab)
	}
	h.press("/")
	if h.model.tab != tabTodos || h.model.layer != layerSearch {
		t.Errorf("tab=%v layer=%v, want todos + search", h.model.tab, h.model.layer)
	}
}

func TestPollRefreshesQuietly(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	f.todos = append(f.todos, api.Todo{ID: "t9", ListID: "l1", Title: "Added on another device", UpdatedAt: time.Now(), CreatedAt: time.Now()})
	h.mustNotContain("Added on another device")

	next, cmd := h.model.Update(pollMsg{})
	h.model = next.(*Model)
	if h.model.busy {
		t.Error("a background refresh should not show the busy indicator")
	}
	h.run(cmd)
	h.mustContain("Added on another device")
}

func TestPollWaitsWhileAFormIsOpen(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("n")
	f.todos = append(f.todos, api.Todo{ID: "t9", ListID: "l1", Title: "Added on another device", UpdatedAt: time.Now(), CreatedAt: time.Now()})
	next, cmd := h.model.Update(pollMsg{})
	h.model = next.(*Model)
	h.run(cmd)
	h.press("esc")
	h.mustNotContain("Added on another device")
}

func TestListPicker(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("F")
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want the picker form", h.model.layer)
	}
	h.mustContain("Show list")
	h.press("right", "right", "enter")
	if h.model.todos.filter.listID != "l2" {
		t.Errorf("listID = %q, want l2 (Household)", h.model.todos.filter.listID)
	}
}

func TestLoadMoreWithNothingMoreSaysSo(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("L")
	h.mustContain("already loaded")
}

func TestKeysCanBeRemapped(t *testing.T) {
	h := newHarnessWith(t, sampleAPI(), func(c *config.Config) { c.Keys = map[string]string{"J": "j", "K": "k"} })
	h.press("g")
	first := h.model.todos.selected().ID
	h.press("J")
	if h.model.todos.selected().ID == first {
		t.Error("J should move down, as j does")
	}
	h.press("K")
	if h.model.todos.selected().ID != first {
		t.Error("K should move back up")
	}
}

func TestRemapKeyParsing(t *testing.T) {
	cases := []struct{ from, to, want string }{
		{"J", "j", "j"},
		{"x", "space", " "},
		{"ctrl+n", "down", "down"},
		{"Q", "ctrl+c", "ctrl+c"},
		{"a", "not a key", "a"}, // unknown target: the key is left alone
	}
	for _, c := range cases {
		from, _ := parseKey(c.from)
		got := remapKey(map[string]string{c.from: c.to}, from).String()
		if got != c.want {
			t.Errorf("%q -> %q: got %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestMouseIsOffUnlessAskedFor(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("g")
	first := h.model.todos.selected().ID
	h.send(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if h.model.todos.selected().ID != first {
		t.Error("the wheel should do nothing when the mouse is not enabled")
	}
}

func TestMouseWheelAndClick(t *testing.T) {
	h := newHarnessWith(t, sampleAPI(), func(c *config.Config) { c.Mouse = true })
	h.press("g")
	first := h.model.todos.selected().ID
	h.send(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if h.model.todos.selected().ID == first {
		t.Error("the wheel should move the cursor")
	}

	// Click the row that holds "Buy milk". The list starts under the header
	// and its own frame, to the right of the sidebar.
	top := headerRows + 1
	row := -1
	for i, l := range h.model.todos.lines {
		if !l.header && h.model.todos.items[l.index].Title == "Buy milk" {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("Buy milk is not in the list")
	}
	h.send(tea.MouseMsg{X: sidebarWidth + 4, Y: top + row - h.model.todos.rows.offset, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if got := h.model.todos.selected().Title; got != "Buy milk" {
		t.Errorf("clicked row = %q, want Buy milk", got)
	}

	// Clicking a tab switches to it.
	h.send(tea.MouseMsg{X: tabX(tabLists) + 1, Y: tabsRow, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h.run(h.cmds...)
	h.cmds = nil
	if h.model.tab != tabLists {
		t.Errorf("tab = %v, want lists", h.model.tab)
	}
}

func TestAssigneeIsNamed(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.model.user.ID = "u1"
	h.model.acct.memberCache["l1"] = []api.Member{{UserID: "u2", Email: "sam@example.com"}}
	if got := h.model.memberLabel("l1", "u2"); got != "sam@example.com" {
		t.Errorf("label = %q, want the email", got)
	}
	if got := h.model.memberLabel("l1", "u1"); got != "you" {
		t.Errorf("label = %q, want you", got)
	}
	if got := h.model.memberLabel("l1", "0123456789"); got != "01234567" {
		t.Errorf("label = %q, want a short id", got)
	}
}

func TestAssigneeIsMarkedOnce(t *testing.T) {
	h := newHarness(t, sampleAPI())
	m := h.model
	m.user.ID = "u1"
	sam := "sam"
	m.acct.memberCache["l1"] = []api.Member{
		{UserID: "u2", Username: &sam, Email: "s***@example.com"},
		{UserID: "u3", Email: "j***@example.com"},
	}
	for _, c := range []struct{ id, want string }{
		{"u2", "@sam"},
		{"u3", "@ j***@example.com"},
		{"u1", "@ you"},
	} {
		todo := api.Todo{ID: "t9", ListID: "l1", Title: "Plan", AssigneeID: &c.id}
		pane := stripANSI(m.todos.todoCard(todo, nil, true, 60, 20))
		if !strings.Contains(pane, c.want) || strings.Contains(pane, "@ @") {
			t.Errorf("pane for %s = %q, want %q once", c.id, pane, c.want)
		}
		// Rows and board cards carry only the mark, as a flag after the title.
		card := stripANSI(strings.Join(m.todos.card(todo, false, 40), "\n"))
		if strings.Count(card, "@") != 1 {
			t.Errorf("card for %s = %q, want one at sign", c.id, card)
		}
		row := stripANSI(m.todoRow(&m.todos, todo, false, 80))
		if strings.Count(row, "@") != 1 {
			t.Errorf("row for %s = %q, want one at sign", c.id, row)
		}
	}
}

func TestEditAcceptsWordsForDates(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "e")
	// title, list, notes, then the "do day" field.
	for i := 0; i < 3; i++ {
		h.press("tab")
	}
	for _, r := range "friday" {
		h.press(string(r))
	}
	h.press("enter")
	if h.model.layer != layerNone {
		t.Fatalf("the form should have closed, layer = %v", h.model.layer)
	}
	if f.writes == 0 {
		t.Error("words in a date field should be parsed and saved, not rejected")
	}
	h.mustNotContain("could not read")
}

// A day alone in words is a day for every date field, as the command line
// reads it: the service answers "friday" with a do date at midnight and no
// time.
func TestEditTakesABareDayInWordsForEveryDateField(t *testing.T) {
	for _, c := range []struct {
		tabs  int
		field string
		want  map[string]string
	}{
		{3, "scheduledDay", map[string]string{"scheduledDay": `"2026-03-13"`}},
		{4, "doAt", map[string]string{"doAt": `"2026-03-13T00:00:00Z"`, "hasTime": "false"}},
		{5, "dueAt", map[string]string{"dueAt": `"2026-03-13T00:00:00Z"`}},
		{6, "remindAt", map[string]string{"remindAt": `"2026-03-13T00:00:00Z"`}},
	} {
		f := sampleAPI()
		f.parsed = map[string]any{"title": "friday", "doAt": "2026-03-13T00:00:00Z", "hasTime": false}
		h := newHarness(t, f)
		h.press("g", "e")
		for i := 0; i < c.tabs; i++ {
			h.press("tab")
		}
		for _, r := range "friday" {
			h.press(string(r))
		}
		h.press("enter")
		h.mustNotContain("could not read")
		if len(f.patches) != 1 {
			t.Errorf("%s: patches = %d, want one", c.field, len(f.patches))
			continue
		}
		body := f.patches[0].body
		if len(body) != len(c.want) {
			t.Errorf("%s: patch = %v, want only %v", c.field, body, c.want)
		}
		for k, v := range c.want {
			if got := string(body[k]); got != v {
				t.Errorf("%s: %s = %s, want %s", c.field, k, got, v)
			}
		}
	}
}

// The new-todo form's reminder takes words too, which the service cannot
// read for itself: it takes a reminder only as a time.
func TestNewTodoReadsWordsForTheReminder(t *testing.T) {
	f := sampleAPI()
	f.parsed = map[string]any{"title": "tomorrow 8am", "doAt": "2026-03-13T08:00:00Z", "hasTime": true}
	h := newHarness(t, f)
	h.press("n", "H", "i")
	for i := 0; i < 3; i++ {
		h.press("tab")
	}
	for _, r := range "tomorrow 8am" {
		h.press(string(r))
	}
	h.press("enter")
	h.mustNotContain("could not read")
	if len(f.created) != 1 || f.created[0].RemindAt == nil || *f.created[0].RemindAt != "2026-03-13T08:00:00Z" {
		t.Fatalf("created = %+v, want one with the reminder read from the words", f.created)
	}

	// Words with no date create nothing.
	f.parsed = map[string]any{"title": "blorp"}
	h.press("n", "H", "o")
	for i := 0; i < 3; i++ {
		h.press("tab")
	}
	for _, r := range "blorp" {
		h.press(string(r))
	}
	h.press("enter")
	h.mustContain("could not read")
	if len(f.created) != 1 {
		t.Errorf("created %d todos, want the unreadable reminder to create none", len(f.created))
	}
}

func TestSetDateIfChangedFlagsWords(t *testing.T) {
	loc := time.UTC
	var p api.TodoPatch
	if w := setDateIfChanged(&p, dates.Day, "2026-03-10", "", loc); w != "" || p.ScheduledDay == nil {
		t.Errorf("a real date should be stored: words=%q patch=%s", w, p.ScheduledDay)
	}
	var q api.TodoPatch
	if w := setDateIfChanged(&q, dates.Day, "next friday", "", loc); w != "next friday" || q.ScheduledDay != nil {
		t.Errorf("words should be handed back, not stored: words=%q patch=%s", w, q.ScheduledDay)
	}
	var r api.TodoPatch
	if w := setDateIfChanged(&r, dates.Day, "", "2026-03-10", loc); w != "" || string(r.ScheduledDay) != "null" {
		t.Errorf("empty should clear: words=%q patch=%s", w, r.ScheduledDay)
	}
}

// The service refuses a date and time without an offset, so the form writes
// one on the account's clock.
func TestSetDateIfChangedWritesOffsets(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	var p api.TodoPatch
	setDateIfChanged(&p, dates.DueAt, "2026-03-10T15:00", "", madrid)
	setDateIfChanged(&p, dates.Remind, "2026-03-30T15:00", "", madrid)
	setDateIfChanged(&p, dates.DoAt, "2026-03-10", "", madrid)
	setDateIfChanged(&p, dates.Day, "2026-03-10T15:00", "", madrid)
	got := map[string]string{
		"due": string(p.DueAt), "remind": string(p.RemindAt), "do": string(p.DoAt),
		"hasTime": string(p.HasTime), "day": string(p.ScheduledDay),
	}
	want := map[string]string{
		"due": `"2026-03-10T15:00:00+01:00"`, "remind": `"2026-03-30T15:00:00+02:00"`,
		"do": `"2026-03-10T00:00:00+01:00"`, "hasTime": "false", "day": `"2026-03-10"`,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %s, want %s", k, got[k], w)
		}
	}
}

func TestRestoreOfKeepsWhatWasOnScreen(t *testing.T) {
	day := "2026-03-10"
	notes := "n"
	at := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	n := restoreOf(api.Todo{Title: "x", ListID: "l1", Notes: &notes, ScheduledDay: &day, DoAt: &at, IsCompleted: true, CompletedAt: &at})
	if n.Title != "x" || n.ListID != "l1" || n.Notes == nil || n.ScheduledDay == nil {
		t.Errorf("restore lost fields: %+v", n)
	}
	if n.DoAt == nil || !strings.HasPrefix(*n.DoAt, "2026-03-10T09:00") {
		t.Errorf("doAt = %v", n.DoAt)
	}
	if n.IsCompleted == nil || !*n.IsCompleted {
		t.Error("a completed todo should come back completed")
	}
}

// -- incremental refresh and the disk cache ---------------------------------

func TestCompletingDoesNotReloadTheList(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	if f.loads != 1 {
		t.Fatalf("startup loads = %d, want 1", f.loads)
	}
	h.press("g", "space")
	if f.loads != 1 || f.feeds != 0 {
		t.Errorf("loads=%d feeds=%d after completing, want the answer merged with no refetch", f.loads, f.feeds)
	}
	var done bool
	for _, todo := range h.model.todos.items {
		if todo.ID == "t1" {
			done = todo.Completed()
		}
	}
	if !done {
		t.Error("the completion should be on screen")
	}
}

func TestDeletingDoesNotReloadTheList(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "d", "enter")
	if f.loads != 1 || f.feeds != 0 {
		t.Errorf("loads=%d feeds=%d after deleting, want none extra", f.loads, f.feeds)
	}
	for _, todo := range h.model.todos.items {
		if todo.ID == "t1" {
			t.Error("the deleted todo should be gone from the list")
		}
	}
}

func TestCreatingCatchesUpThroughTheFeed(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("n")
	for _, r := range "Water the plants" {
		h.press(string(r))
	}
	h.press("enter")
	if f.loads != 1 {
		t.Errorf("loads = %d, want the startup load only", f.loads)
	}
	if f.feeds != 1 {
		t.Errorf("feeds = %d, want one catch-up", f.feeds)
	}
	h.mustContain("Water the plants")
}

func TestPollUsesTheFeed(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	next, cmd := h.model.Update(pollMsg{})
	h.model = next.(*Model)
	h.run(cmd)
	if f.loads != 1 || f.feeds != 1 {
		t.Errorf("loads=%d feeds=%d, want the poll to be one feed pull", f.loads, f.feeds)
	}
}

func TestNarrowedViewsStillLoadFromTheServer(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("/", "p", "a", "s", "s", "enter") // a search is answered by the server
	if f.loads != 2 {
		t.Fatalf("loads = %d, want the search to ask the server", f.loads)
	}
	if h.model.todos.cursor != "" {
		t.Error("a filtered view has no feed cursor")
	}
	h.press("g", "space")
	if f.loads != 3 {
		t.Errorf("loads = %d, want a reload after acting in a filtered view", f.loads)
	}
	h.press("/", "esc") // clear the search
	if h.model.todos.cursor == "" {
		t.Error("the whole-account view should have a cursor again")
	}
}

func TestManualRefreshReloadsEverything(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("r")
	if f.loads != 2 {
		t.Errorf("loads = %d, want r to be a full reload", f.loads)
	}
}

func TestFeedAppliesChangesAndDeletions(t *testing.T) {
	h := newHarness(t, sampleAPI())
	now := time.Now()
	gone := now
	h.model.todos.applyFeed([]api.SyncTodo{
		{Todo: api.Todo{ID: "t2", ListID: "l2", Title: "Renew passport (renamed)", UpdatedAt: now}},
		{Todo: api.Todo{ID: "t1", DeletedAt: &gone}},
		{Todo: api.Todo{ID: "t_new", ListID: "l1", Title: "Brand new", UpdatedAt: now}},
	})
	h.mustContain("Renew passport (renamed)")
	h.mustContain("Brand new")
	h.mustNotContain("Pay the cable bill")
}

func TestStaleFeedReplyIsIgnored(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.model.todos.seq += 3
	before := len(h.model.todos.items)
	h.send(feedMsg{seq: h.model.todos.seq - 1, changes: []api.SyncTodo{{Todo: api.Todo{ID: "x", Title: "stale"}}}})
	if len(h.model.todos.items) != before {
		t.Error("a stale feed reply changed the list")
	}
}

func TestFeedFailureFallsBackToAFullLoad(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.model.todos.seq++
	next, cmd := h.model.Update(feedMsg{seq: h.model.todos.seq, err: &api.Error{StatusCode: 500, Code: api.CodeInternal}})
	h.model = next.(*Model)
	if h.model.todos.cursor != "" {
		t.Error("the cursor should be dropped")
	}
	h.run(cmd)
	if f.loads != 2 {
		t.Errorf("loads = %d, want a full load after the feed failed", f.loads)
	}
}

// -- command palette and quick reschedule -----------------------------------

func TestPaletteFindsAndRunsAnAction(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press(":")
	if h.model.layer != layerPalette {
		t.Fatalf("layer = %v, want the palette", h.model.layer)
	}
	h.mustContain("Command palette")
	for _, r := range "quick" {
		h.press(string(r))
	}
	if got := len(h.model.palette.matches); got != 1 {
		t.Fatalf("matches = %d, want one for %q", got, "quick")
	}
	h.press("enter")
	if h.model.layer != layerForm || h.model.form.id != formQuickAdd {
		t.Errorf("layer=%v: the quick capture form should be open", h.model.layer)
	}
}

func TestPaletteSwitchesTabForTheAction(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("2")
	h.press("ctrl+p")
	for _, r := range "new todo" {
		h.press(string(r))
	}
	h.press("enter")
	if h.model.tab != tabTodos || h.model.layer != layerForm {
		t.Errorf("tab=%v layer=%v, want Todos with the new-todo form", h.model.tab, h.model.layer)
	}
}

func TestPaletteIgnoresKeyRemapping(t *testing.T) {
	h := newHarnessWith(t, sampleAPI(), func(c *config.Config) { c.Keys = map[string]string{"n": "x"} })
	h.press(":")
	for _, r := range "new todo" {
		h.press(string(r))
	}
	h.press("enter")
	if h.model.layer != layerForm {
		t.Errorf("layer = %v: the palette should run the action whatever n is bound to", h.model.layer)
	}
}

func TestPaletteEscAndEmptyResult(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press(":")
	for _, r := range "zzzz" {
		h.press(string(r))
	}
	h.mustContain("nothing matches")
	h.press("enter") // nothing selected: stays open
	if h.model.layer != layerPalette {
		t.Error("enter on no match should not close the palette")
	}
	h.press("esc")
	if h.model.layer != layerNone {
		t.Error("esc should close it")
	}
}

func TestEveryPaletteEntryHasAKey(t *testing.T) {
	for _, e := range paletteEntries {
		if _, ok := parseKey(e.key); !ok {
			t.Errorf("%q: %q is not a key the palette can press", e.title, e.key)
		}
	}
}

func TestRescheduleToTomorrow(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "T")
	if len(f.patches) != 1 {
		t.Fatalf("patches = %d, want one", len(f.patches))
	}
	want := h.model.now.In(h.model.loc).AddDate(0, 0, 1).Format("2006-01-02")
	if got := string(f.patches[0].body["scheduledDay"]); got != `"`+want+`"` {
		t.Errorf("scheduledDay = %s, want %q", got, want)
	}
	if _, touched := f.patches[0].body["dueAt"]; touched {
		t.Error("rescheduling should only change the day")
	}
	h.mustContain("moved to tomorrow")
}

func TestRescheduleMarkedTodos(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "v", "v", "w")
	if len(f.patches) != 2 {
		t.Fatalf("patches = %d, want one per marked todo", len(f.patches))
	}
	h.mustContain("rescheduled to next week")
}

// A cursor is a place in the service's log. It is kept as it came, not moved
// back to allow for clock drift, which means nothing for a position.
func TestTheFeedCursorIsKeptExactlyAsTheServiceGaveIt(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	if h.model.todos.cursor == "" || !h.model.todos.whole {
		t.Fatalf("cursor=%q whole=%v, want the sync that read the account to give both", h.model.todos.cursor, h.model.todos.whole)
	}
	h.model.todos.seq++
	next, _ := h.model.Update(feedMsg{seq: h.model.todos.seq, cursor: "40412"})
	h.model = next.(*Model)
	if h.model.todos.cursor != "40412" {
		t.Errorf("cursor = %q, want the one the feed returned", h.model.todos.cursor)
	}
}

// listField is the options and value of a form's list picker.
func listField(t *testing.T, h *harness) ([]string, string) {
	t.Helper()
	if h.model.form == nil {
		t.Fatal("no form is open")
	}
	for _, fl := range h.model.form.fields {
		if fl.key == "list" {
			return fl.options, fl.value
		}
	}
	t.Fatal("the form has no list field")
	return nil, ""
}

// A read-only list takes no new todos, so no form offers to put one there,
// where every request would be refused. A todo already in one still shows
// its own list, and saving it does not move it.
func TestReadOnlyListsAreNotOfferedAsDestinations(t *testing.T) {
	f := sampleAPI()
	past := time.Now().Add(-48 * time.Hour)
	f.todos = append(f.todos, api.Todo{ID: "t4", ListID: "l3", Title: "File the report", UpdatedAt: past, CreatedAt: past})
	h := newHarness(t, f)

	h.press("n")
	if opts, _ := listField(t, h); slices.Contains(opts, "Work") || len(opts) != 2 {
		t.Errorf("new todo offers %q, want the two writable lists", opts)
	}
	h.press("esc", "g", "v", "m")
	if opts, _ := listField(t, h); slices.Contains(opts, "Work") || len(opts) != 2 {
		t.Errorf("move offers %q, want the two writable lists", opts)
	}
	h.press("esc", "esc")

	h.model.todos.reveal("t4")
	h.press("e")
	opts, value := listField(t, h)
	if value != "Work" || !slices.Contains(opts, "Work") {
		t.Errorf("edit shows list %q of %q, want the todo's own list, Work", value, opts)
	}
	h.press("ctrl+s")
	for _, p := range f.patches {
		if _, moved := p.body["listId"]; moved {
			t.Errorf("saving an untouched edit moved the todo: %v", p.body)
		}
	}
}
