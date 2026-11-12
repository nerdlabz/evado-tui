package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
)

// startedOnHousehold opens the app on one list, the way a remembered list
// or saved view does, so the todos asked for are not the whole account.
func startedOnHousehold(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, sampleAPI(), func(c *config.Config) { c.ListID = "l2" })
}

func TestCountsDescribeTheWholeAccountWhenStartedOnOneList(t *testing.T) {
	h := startedOnHousehold(t)
	if got := h.model.todos.filter.listID; got != "l2" {
		t.Fatalf("listID = %q, the test needs a narrowed start", got)
	}
	if len(h.model.todos.all) != 1 {
		t.Fatalf("loaded %d todos, want only Household's one", len(h.model.todos.all))
	}
	s := h.model.todos.stats()
	if s.open != 2 || s.done != 1 {
		t.Errorf("open/done = %d/%d, want 2/1 for the whole account", s.open, s.done)
	}
	if s.perList["l1"] != 1 || s.perList["l2"] != 1 {
		t.Errorf("perList = %v, want one open in Personal and one in Household", s.perList)
	}
	if got := h.model.countsView(); !strings.Contains(got, "2 open") {
		t.Errorf("header = %q, want 2 open", got)
	}

	h.press("F", "right", "right", "enter") // All lists
	if s := h.model.todos.stats(); s.open != 2 || s.done != 1 {
		t.Errorf("after changing the view open/done = %d/%d, want 2/1", s.open, s.done)
	}
}

func TestNarrowedViewKeepsCountsCurrentAfterAChange(t *testing.T) {
	h := startedOnHousehold(t)
	h.press("g", "space") // complete Renew passport
	s := h.model.todos.stats()
	if s.open != 1 || s.done != 2 {
		t.Errorf("open/done = %d/%d, want 1/2 after completing a todo", s.open, s.done)
	}
}

func TestListsTabCountsEveryListWhileTodosIsNarrowed(t *testing.T) {
	h := startedOnHousehold(t)
	h.press("2")
	h.mustContain("1 open")
	h.mustContain("1 done")
	if strings.Contains(h.view(), "nothing in this list yet") {
		t.Error("Personal has todos but the Lists tab says it is empty")
	}
}

// sectionShown is the heading a todo is listed under.
func sectionShown(v *todosView, id string) string {
	heading := ""
	for _, l := range v.lines {
		switch {
		case l.header:
			heading = l.label
		case v.items[l.index].ID == id:
			return heading
		}
	}
	return ""
}

// A todo moves to Overdue when its due time passes and when its day ends,
// in the list as well as in the counts, with nothing else happening.
func TestSectionsFollowTheClock(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	loc := h.model.loc
	day := time.Date(2030, 3, 10, 9, 0, 0, 0, loc)
	due := time.Date(2030, 3, 10, 15, 0, 0, 0, loc)
	planned := day.Format("2006-01-02")
	f.todos = []api.Todo{
		{ID: "planned", ListID: "l1", Title: "Planned for the day", ScheduledDay: &planned, UpdatedAt: day, CreatedAt: day},
		{ID: "due", ListID: "l1", Title: "Due in the afternoon", DueAt: &due, UpdatedAt: day, CreatedAt: day},
	}
	// The list as it was read that morning.
	h.model.now = day
	h.run(h.model.todos.load())
	v := &h.model.todos
	for _, id := range []string{"planned", "due"} {
		if got := sectionShown(v, id); got != "Today" {
			t.Fatalf("%s is under %q in the morning, want Today", id, got)
		}
	}

	h.send(tickMsg(due.Add(time.Second)))
	if got := sectionShown(v, "due"); got != "Overdue" {
		t.Errorf("once its due time passed, due is under %q, want Overdue", got)
	}
	if got := sectionShown(v, "planned"); got != "Today" {
		t.Errorf("in the afternoon, planned is under %q, want Today", got)
	}

	h.send(tickMsg(time.Date(2030, 3, 11, 0, 0, 1, 0, loc)))
	if got := sectionShown(v, "planned"); got != "Overdue" {
		t.Errorf("after midnight, planned is under %q, want Overdue", got)
	}
	if s := v.stats(); s.overdue != 2 {
		t.Errorf("overdue count = %d, want 2, as the list shows", s.overdue)
	}
}
