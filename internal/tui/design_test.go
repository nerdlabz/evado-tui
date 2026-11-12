package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
)

func TestSidebarPicksAView(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.mustContain("Views")
	h.press("h")
	if h.model.focus != focusSidebar {
		t.Fatalf("focus = %v, want the sidebar", h.model.focus)
	}
	h.press("j", "enter")
	v := h.model.todos
	if v.filter.mode != viewStatus || v.filter.status != api.StatusOverdue {
		t.Errorf("view = %v %q, want the overdue filter", v.filter.mode, v.filter.status)
	}
	if h.model.focus != focusList {
		t.Errorf("focus = %v, want the list back", h.model.focus)
	}
}

func TestSidebarNarrowsToAList(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("h", "G", "k", "enter") // the last list is Work, so this is Household
	if got := h.model.todos.filter.listID; got != "l2" {
		t.Fatalf("list filter = %q, want l2", got)
	}
	h.press("]")
	if got := h.model.todos.filter.listID; got != "l3" {
		t.Errorf("] moved to %q, want l3", got)
	}
	h.press("]")
	if got := h.model.todos.filter.listID; got != "" {
		t.Errorf("] past the last list = %q, want every list", got)
	}
}

// A key the sidebar has no use for belongs to the todos, so typing n with
// the sidebar focused still opens the new todo form.
func TestSidebarPassesOtherKeysToTheTodos(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("h", "n")
	if h.model.layer != layerForm {
		t.Errorf("layer = %v, want the new todo form", h.model.layer)
	}
}

// Narrowing the terminal takes the sidebar away, and its keys with it:
// enter must not pick a list nobody can see.
func TestSidebarLetsGoWhenAResizeHidesIt(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("h")
	if h.model.focus != focusSidebar {
		t.Fatalf("focus = %v, want the sidebar", h.model.focus)
	}
	h.send(tea.WindowSizeMsg{Width: 80, Height: 30})
	h.mustNotContain("Views")
	if h.model.focus != focusList {
		t.Fatalf("focus = %v after the sidebar went, want the list", h.model.focus)
	}
	h.press("G", "enter")
	if got := h.model.todos.filter.listID; got != "" {
		t.Errorf("list filter = %q, want nothing picked out of sight", got)
	}
}

func TestMemberPaneLetsGoWhenAResizeHidesIt(t *testing.T) {
	f := sampleAPI()
	f.members = []api.Member{
		{UserID: "u1", Email: "demo@example.com", Role: "owner"},
		{UserID: "u2", Email: "sam@example.com", Role: "member"},
	}
	h := newHarness(t, f)
	h.press("2", "j", "m", "M")
	if h.model.focus != focusMembers {
		t.Fatalf("focus = %v, want members", h.model.focus)
	}
	// Just tall enough for the first member: the pane keeps the keyboard.
	h.send(tea.WindowSizeMsg{Width: 100, Height: 12})
	h.mustContain("demo@example.com")
	if h.model.focus != focusMembers {
		t.Fatalf("focus = %v with a member still showing, want members", h.model.focus)
	}
	// A row shorter, and the members are cut off the bottom.
	h.send(tea.WindowSizeMsg{Width: 100, Height: 11})
	h.mustNotContain("demo@example.com")
	if h.model.focus != focusList {
		t.Errorf("focus = %v with the members cut off, want the lists", h.model.focus)
	}

	// Stacked on a narrow terminal, the details may have no room at all.
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.press("M")
	h.send(tea.WindowSizeMsg{Width: 20, Height: 30})
	if h.model.focus != focusList {
		t.Errorf("focus = %v with no details pane, want the lists", h.model.focus)
	}
}

func TestBoardMovesATodoToToday(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("b")
	h.mustContain("Board")
	h.mustContain("Tomorrow")

	// u jumps to the inbox, which holds the undated todo.
	h.press("u")
	sel := h.model.todos.selected()
	if sel == nil || sel.ID != "t2" {
		t.Fatalf("board cursor = %+v, want the inbox todo", sel)
	}
	h.press(">")
	if len(f.patches) != 1 {
		t.Fatalf("patches = %d, want one", len(f.patches))
	}
	want := time.Now().UTC().Format("2006-01-02")
	if got := string(f.patches[0].body["scheduledDay"]); got != `"`+want+`"` {
		t.Errorf("scheduledDay = %s, want %q", got, want)
	}
	// The cursor follows the todo into its new column.
	v := h.model.todos
	if got := v.bcols[v.bcol].label; got != "Today" {
		t.Errorf("cursor column = %q, want Today", got)
	}
	if sel := v.selected(); sel == nil || sel.ID != "t2" {
		t.Errorf("cursor left the todo it moved: %+v", sel)
	}
}

// Pushing a late todo forward lands it on today, not on the day after the
// one it missed.
func TestShiftDayBringsALateTodoToToday(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", ">")
	if len(f.patches) != 1 || f.patches[0].id != "t1" {
		t.Fatalf("patches = %+v, want one for t1", f.patches)
	}
	want := time.Now().UTC().Format("2006-01-02")
	if got := string(f.patches[0].body["scheduledDay"]); got != `"`+want+`"` {
		t.Errorf("scheduledDay = %s, want %q", got, want)
	}
}

func TestBoardSurvivesEveryWidth(t *testing.T) {
	for _, size := range [][2]int{{30, 8}, {60, 14}, {100, 30}, {220, 50}} {
		h := newHarness(t, sampleAPI())
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		h.press("b", "l", "l", "j", "h", "enter", "esc")
		for _, line := range strings.Split(h.view(), "\n") {
			if w := lipgloss.Width(line); w > size[0] {
				t.Fatalf("%dx%d: line is %d cells wide: %q", size[0], size[1], w, line)
			}
		}
	}
}

// A todo being ticked off stays where it is until the animation ends, and
// only then moves to Done.
func TestCompletionHoldsItsPlaceWhileAnimating(t *testing.T) {
	animDur = time.Hour
	t.Cleanup(func() { animDur = 0 })
	h := newHarness(t, sampleAPI())
	h.press("g", "space")
	v := &h.model.todos
	var done api.Todo
	for _, todo := range v.items {
		if todo.ID == "t1" {
			done = todo
		}
	}
	if !done.Completed() {
		t.Fatal("the todo was not completed")
	}
	if got := v.sectionOf(done); got == "done" {
		t.Error("the todo moved to Done before its animation ended")
	}
	animDur = 0
	v.reapAnims()
	if got := v.sectionOf(done); got != "done" {
		t.Errorf("section after the animation = %q, want done", got)
	}
	if len(v.anims) != 0 {
		t.Errorf("anims = %d, want none left", len(v.anims))
	}
}

func TestThemePickerAppliesAndSaves(t *testing.T) {
	t.Cleanup(func() { applyTheme("") })
	h := newHarness(t, sampleAPI())
	before := current.name
	h.send(tea.KeyMsg{Type: tea.KeyCtrlT})
	h.mustContain("Tokyo Night")

	// Moving the cursor applies the theme straight away; esc puts it back.
	h.press("j", "j", "j")
	if current.name == before {
		t.Error("moving through the picker did not change the theme")
	}
	h.press("esc")
	if current.name != before {
		t.Errorf("esc left the theme at %q, want %q", current.name, before)
	}

	h.send(tea.KeyMsg{Type: tea.KeyCtrlT})
	h.press("k", "enter") // wraps to the last theme
	want := schemes[len(schemes)-1].name
	if current.name != want {
		t.Errorf("theme = %q, want %q", current.name, want)
	}
	disk, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if disk.Theme != want {
		t.Errorf("saved theme = %q, want %q", disk.Theme, want)
	}
	if disk.Token != "" {
		t.Error("saving the theme must not write the token given for this run")
	}
}

func TestEveryThemeIsComplete(t *testing.T) {
	for _, s := range schemes {
		for name, c := range map[string]string{
			"fg": s.fg, "muted": s.muted, "faint": s.faint, "accent": s.accent, "accent2": s.accent2,
			"green": s.green, "red": s.red, "amber": s.amber, "blue": s.blue, "panel": s.panel, "border": s.border,
		} {
			if len(c) != 7 || c[0] != '#' {
				t.Errorf("theme %s: %s = %q, want a #rrggbb colour", s.name, name, c)
			}
		}
	}
}

func TestFrameIsExactlyItsSize(t *testing.T) {
	for _, size := range [][2]int{{6, 3}, {20, 5}, {40, 12}} {
		out := frame("some body text\nthat runs on for a while longer than the frame", size[0], size[1],
			frameOpts{title: "A rather long title", foot: "a foot"})
		lines := strings.Split(out, "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		for _, l := range lines {
			if w := lipgloss.Width(l); w != size[0] {
				t.Errorf("%v: line is %d cells wide: %q", size, w, l)
			}
		}
	}
}

// The new todo form shows how the server reads the title, and drops an
// answer to text that has since changed.
func TestNewTodoFormShowsTheLiveReading(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("n")
	day := "2026-03-10"
	h.model.previewSeq = 4
	h.send(previewMsg{seq: 3, preview: api.ParsePreview{ScheduledDay: &day}})
	h.mustNotContain("→")
	h.send(previewMsg{seq: 4, preview: api.ParsePreview{ScheduledDay: &day}})
	h.mustContain("→ on ")
}

// Text that names a day the reader could not place says so, instead of
// showing an inbox result as if nothing were wrong.
func TestNewTodoFormSaysWhenADateWasNotUnderstood(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("n")
	h.model.previewSeq = 1
	h.send(previewMsg{seq: 1, preview: api.ParsePreview{Title: "Book flights in a fortnight"}})
	h.mustContain(`"in a fortnight" was not understood as a date`)

	h.model.previewSeq = 2
	h.send(previewMsg{seq: 2, preview: api.ParsePreview{Title: "Buy milk"}})
	h.mustNotContain("not understood")
	h.mustContain("no date, so it goes to the inbox")
}

// A time the reader had to guess the half of the day for says so, and how to
// say the other one.
func TestNewTodoFormSaysWhenATimeWasAssumed(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("n")
	at := time.Date(2026, 3, 10, 9, 0, 0, 0, h.model.loc)
	day := "2026-03-10"
	h.model.previewSeq = 1
	h.send(previewMsg{seq: 1, preview: api.ParsePreview{
		Title: "Call", ScheduledDay: &day, DoAt: &at, HasTime: true,
		AssumedTime: &api.AssumedTime{Text: "at 9", Assumed: "am", AlternativeText: "at 9pm", AlternativeDoAt: at.Add(12 * time.Hour)},
	}})
	h.mustContain("at 09:00 (assumed; type 9pm for evening)")

	h.model.previewSeq = 2
	h.send(previewMsg{seq: 2, preview: api.ParsePreview{Title: "Call", ScheduledDay: &day, DoAt: &at, HasTime: true}})
	h.mustNotContain("assumed")
}

func TestStatsCountTheRecentRecord(t *testing.T) {
	f := sampleAPI()
	now := time.Now()
	for i, ago := range []int{0, 1, 2, 5} {
		at := now.AddDate(0, 0, -ago)
		f.todos = append(f.todos, api.Todo{
			ID: "d" + string(rune('a'+i)), ListID: "l1", Title: "done", IsCompleted: true,
			CompletedAt: &at, CreatedAt: at, UpdatedAt: at,
		})
	}
	h := newHarness(t, f)
	s := h.model.todos.stats()
	if s.streak != 3 {
		t.Errorf("streak = %d, want 3", s.streak)
	}
	if s.doneToday != 1 {
		t.Errorf("done today = %d, want 1", s.doneToday)
	}
	if s.open != 2 || s.perList["l1"] != 1 || s.perList["l2"] != 1 {
		t.Errorf("open = %d per list %v, want one in each of two lists", s.open, s.perList)
	}
}

// The Account sections wrap their notes to fit the pane. A note wider than
// the pane makes the frame clip every row, which shows as a "…" against the
// right border.
func TestAccountSectionsFitTheirPane(t *testing.T) {
	for _, width := range []int{80, 110, 160} {
		h := newHarness(t, sampleAPI())
		h.send(tea.WindowSizeMsg{Width: width, Height: 50})
		h.press("4")
		for section := 0; section < 10; section++ {
			for _, line := range strings.Split(h.view(), "\n") {
				if strings.HasSuffix(strings.TrimRight(ansi.Strip(line), " "), "…│") {
					t.Fatalf("width %d, section %d: a row is clipped: %q", width, section, ansi.Strip(line))
				}
			}
			h.press("j")
		}
	}
}
