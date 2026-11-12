package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/testapi"
)

// pageOf cuts the todos a filtered list matched to the page the query asks
// for, the way the service does: at most limit of them (100 unless said, and
// refused above 200), after the todo the cursor names, with the cursor for
// the page after, or nil on the last. ok is false for a request the service
// would refuse.
func pageOf(q url.Values, matched []api.Todo) (page []api.Todo, next any, ok bool) {
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > api.MaxPageSize {
			return nil, nil, false
		}
		limit = n
	}
	start := 0
	if q.Has("cursor") {
		parts := strings.Split(q.Get("cursor"), ":")
		if len(parts) != 3 {
			return nil, nil, false
		}
		start = len(matched)
		for i, t := range matched {
			if t.ID == parts[2] {
				start = i + 1
				break
			}
		}
	}
	rest := matched[start:]
	if len(rest) <= limit {
		return rest, nil, true
	}
	return rest[:limit], testapi.PageCursor(rest[limit-1]), true
}

// crowdOf adds n open todos to the list l1 of the fake, dated by the day so
// that they keep a fixed order.
func crowdOf(f *fakeAPI, n int) {
	for i := range n {
		f.todos = append(f.todos, api.Todo{
			ID: fmt.Sprintf("p%04d", i), ListID: "l1", Title: fmt.Sprintf("Paged %04d", i),
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		})
	}
}

// pagedQueries are the filtered reads the fake saw, as "limit/cursor" pairs
// with the cursor cut to the id of the todo it names.
func pagedQueries(f *fakeAPI) []string {
	var out []string
	for _, q := range f.queries {
		cur := q.Get("cursor")
		cur = cur[strings.LastIndex(cur, ":")+1:]
		out = append(out, q.Get("limit")+"/"+cur)
	}
	return out
}

// A filtered list of more than a page says so, and L reads the page the
// service pointed at and puts it after what is there.
func TestLoadMoreFollowsTheCursorPageByPage(t *testing.T) {
	f := sampleAPI()
	crowdOf(f, 450)
	h := newHarnessWith(t, f, func(c *config.Config) { c.ListID = "l1" })
	v := &h.model.todos

	if len(v.all) != 200 || !v.moreToLoad() {
		t.Fatalf("loaded %d, more = %v, want a first page of 200 that says there is more", len(v.all), v.moreToLoad())
	}
	h.mustContain("showing the first 200")
	h.mustContain("L for more")

	h.press("L")
	if len(v.all) != 400 || !v.moreToLoad() {
		t.Fatalf("after L: %d loaded, more = %v, want 400 and still more", len(v.all), v.moreToLoad())
	}
	h.mustContain("showing the first 400")

	h.press("L")
	if len(v.all) != 452 || v.moreToLoad() {
		t.Fatalf("after L again: %d loaded, more = %v, want everything", len(v.all), v.moreToLoad())
	}
	h.mustNotContain("showing the first")
	seen := map[string]bool{}
	for _, t2 := range v.all {
		if seen[t2.ID] {
			t.Fatalf("%s was loaded twice", t2.ID)
		}
		seen[t2.ID] = true
	}

	last := pagedQueries(f)
	if got := last[len(last)-2:]; got[0][:4] != "200/" || got[0] == "200/" || got[1][:4] != "200/" || got[0] == got[1] {
		t.Errorf("reads = %q, want pages of 200 each after the last todo of the one before", last)
	}

	h.press("L")
	h.mustContain("already loaded")
}

// The server's old answer had no cursor; the notes about its ceiling are gone
// with the ceiling.
func TestNothingSaysTheServerReturnsNoMore(t *testing.T) {
	f := sampleAPI()
	crowdOf(f, 450)
	h := newHarnessWith(t, f, func(c *config.Config) { c.ListID = "l1" })
	h.press("L", "L")
	h.mustNotContain("returns no more")
	h.mustNotContain("most the client")
}

// A reload of the same filters, as every change in a filtered view causes,
// puts back the pages already loaded instead of folding the list to its first.
func TestRefreshKeepsThePagesAlreadyLoaded(t *testing.T) {
	f := sampleAPI()
	crowdOf(f, 450)
	h := newHarnessWith(t, f, func(c *config.Config) { c.ListID = "l1" })
	v := &h.model.todos
	h.press("L")
	if len(v.all) != 400 {
		t.Fatalf("loaded %d, want 400 after L", len(v.all))
	}

	f.queries = nil
	h.run(h.model.refreshAll(true))
	if len(v.all) != 400 || !v.moreToLoad() {
		t.Errorf("after a reload: %d loaded, more = %v, want the 400 back and more still to come", len(v.all), v.moreToLoad())
	}
	if got := pagedQueries(f); len(got) != 2 || got[0] != "200/" || got[1][:4] != "200/" {
		t.Errorf("reload reads = %q, want the two pages again", got)
	}

	h.press("L")
	if len(v.all) != 452 || v.moreToLoad() {
		t.Errorf("L after the reload: %d loaded, more = %v, want everything", len(v.all), v.moreToLoad())
	}
}

// The cursor is only good under the filters it came with, so a new filter
// starts again from its first page.
func TestChangingTheFilterDropsThePagesAndTheirCursor(t *testing.T) {
	f := sampleAPI()
	crowdOf(f, 450)
	h := newHarnessWith(t, f, func(c *config.Config) { c.ListID = "l1" })
	v := &h.model.todos
	h.press("L")
	if len(v.all) != 400 {
		t.Fatalf("loaded %d, want 400 after L", len(v.all))
	}

	f.queries = nil
	h.press("/")
	h.typeText("Paged 00")
	h.press("enter")
	if got := pagedQueries(f); len(got) == 0 || got[0] != "200/" {
		t.Fatalf("search reads = %q, want it to start at the first page with no cursor", got)
	}
	for _, q := range f.queries {
		if q.Get("q") != "" && q.Get("cursor") != "" {
			t.Errorf("a cursor from the earlier filters went with a new search: %v", q)
		}
	}
	if len(v.all) != 100 || v.moreToLoad() {
		t.Errorf("search shows %d, more = %v, want its 100 matches and no more", len(v.all), v.moreToLoad())
	}
}

// A page that arrives for filters the list has since left is dropped, not
// added to the wrong list.
func TestAPageForOtherFiltersIsNotAdded(t *testing.T) {
	f := sampleAPI()
	crowdOf(f, 450)
	h := newHarnessWith(t, f, func(c *config.Config) { c.ListID = "l1" })
	v := &h.model.todos
	before := len(v.all)

	stale := fetchMoreTodos(h.model.ctx, h.model.client, v.filter.request(pageSize), v.seq)().(todosMsg)
	stale.of.ListID = "elsewhere"
	h.send(stale)
	if len(v.all) != before {
		t.Errorf("loaded %d after a page for other filters, want the %d it had", len(v.all), before)
	}
}

// A page that cannot be read leaves what is on screen as it was, and L can be
// pressed again once the service answers.
func TestAFailedPageLeavesTheListAndCanBeRetried(t *testing.T) {
	f := sampleAPI()
	crowdOf(f, 450)
	h := newHarnessWith(t, f, func(c *config.Config) { c.ListID = "l1" })
	v := &h.model.todos
	f.refuse = func(r *http.Request) bool { return r.URL.Query().Has("cursor") }

	h.press("L")
	if len(v.all) != 200 || !v.moreToLoad() {
		t.Fatalf("after a failed page: %d loaded, more = %v, want the first 200 and more still to load", len(v.all), v.moreToLoad())
	}
	h.mustContain("the server fell over")

	f.refuse = nil
	h.press("L")
	if len(v.all) != 400 {
		t.Errorf("after retrying: %d loaded, want 400", len(v.all))
	}
}

// A list that fits in a page says nothing and L has nothing to do.
func TestAShortListHasNoMoreToLoad(t *testing.T) {
	h := newHarness(t, sampleAPI())
	if h.model.todos.moreToLoad() {
		t.Error("a short list says there is more")
	}
	h.mustNotContain("L for more")
}

// The todos on this computer answer the same way, so L pages them too.
func TestLoadMoreOnThisComputerFollowsTheStoresCursor(t *testing.T) {
	personal, _ := seedComputer(t, 450, 5)
	h := restart(t, config.Config{Local: true, ListID: personal})
	v := &h.model.todos
	if len(v.all) != pageSize || !v.moreToLoad() {
		t.Fatalf("loaded %d, more = %v, want a page of %d and more", len(v.all), v.moreToLoad(), pageSize)
	}
	h.press("L", "L")
	if len(v.all) != 450 || v.moreToLoad() {
		t.Errorf("after L twice: %d loaded, more = %v, want all 450", len(v.all), v.moreToLoad())
	}
}

// An account with more todos than one read of it takes in is refused rather
// than shown in part, and the refusal says so in words, with what still
// works, instead of an empty list that reads as every todo being done.
func TestAccountTooBigToReadSaysSo(t *testing.T) {
	if got := groupDigits(accountLimit); got != "10,000" {
		t.Fatalf("limit written as %q, want 10,000", got)
	}
	big := testapi.New()
	big.FeedPageSize = 1 // so the page bound is reached without 10,000 todos
	big.Todos = nil
	for i := range maxFeedPages + 1 {
		big.Todos = append(big.Todos, api.Todo{ID: fmt.Sprintf("b%03d", i), ListID: "l1", Title: "Big"})
	}
	c := big.Client(t)

	h := newHarness(t, &fakeAPI{lists: sampleAPI().lists})
	h.mustContain("Every todo is done")
	msg, ok := fetchAccount(context.Background(), c, h.model.todos.seq)().(todosMsg)
	if !ok || !errors.Is(msg.err, errAccountTooBig) || len(msg.todos) != 0 {
		t.Fatalf("read = %d todos, err %v, want none and the account refused as too big", len(msg.todos), msg.err)
	}
	h.send(msg)
	h.run(h.cmds...)
	h.cmds = nil
	h.mustContain("more than 10,000 todos, the most this version reads at once; F shows one list")
	h.mustContain("This account has more than 10,000 todos,")
	h.mustContain("evado ls -cursor")
	h.mustNotContain("Every todo is done")
	h.mustNotContain("load todos")

	// One list is read a page at a time, so it still loads.
	h.press("F", "right", "enter")
	if h.model.todos.filter.listID != "l1" {
		t.Fatalf("filter = %+v, want the list l1", h.model.todos.filter)
	}
	h.mustContain("Nothing matches these filters")
	h.mustNotContain("This account has more than")

	// One page fewer is read whole.
	big.Todos = big.Todos[:maxFeedPages]
	msg = fetchAccount(context.Background(), c, 0)().(todosMsg)
	if msg.err != nil || len(msg.todos) != maxFeedPages {
		t.Fatalf("read = %d todos, err %v, want all %d", len(msg.todos), msg.err, maxFeedPages)
	}
}
