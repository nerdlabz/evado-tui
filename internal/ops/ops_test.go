package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/testapi"
)

func TestAddSplitsLinesAndResolvesTheList(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	got, err := o.Add(context.Background(), AddInput{Text: "one\ntwo", List: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ListID != "l2" {
		t.Errorf("added %+v, want two todos on the Work list", got)
	}
	if _, err := o.Add(context.Background(), AddInput{Text: "  "}); ExitCode(err) != ExitUsage {
		t.Errorf("empty text: exit %d, want usage", ExitCode(err))
	}
	if _, err := o.Add(context.Background(), AddInput{Text: "x", List: "nope"}); ExitCode(err) != ExitNotFound {
		t.Errorf("unknown list: exit %d, want not found", ExitCode(err))
	}
	if _, err := o.Add(context.Background(), AddInput{Text: "a\nb", ID: "fixed"}); ExitCode(err) != ExitUsage {
		t.Errorf("an id with several lines should be a usage error")
	}
	one, err := o.Add(context.Background(), AddInput{Text: "keep", ID: "fixed"})
	if err != nil || one[0].ID != "fixed" {
		t.Errorf("a caller-chosen id should be used: %+v %v", one, err)
	}
}

func TestListTodosValidatesAndFilters(t *testing.T) {
	o := ops(t, testapi.New())
	ctx := context.Background()
	open, err := o.ListTodos(ctx, ListInput{})
	if err != nil || len(open.Todos) != 2 || open.NextCursor != "" {
		t.Fatalf("default listing = %+v,%v, want the two open todos and nothing more", open, err)
	}
	if _, err := o.ListTodos(ctx, ListInput{Status: "bogus"}); ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "today") {
		t.Errorf("a bad status should list the good ones: %v", err)
	}
	if _, err := o.ListTodos(ctx, ListInput{Assigned: "bob"}); ExitCode(err) != ExitUsage {
		t.Errorf("a bad assigned value should be a usage error")
	}
	work, _ := o.ListTodos(ctx, ListInput{Status: "all", List: "Work"})
	if len(work.Todos) != 1 || work.Todos[0].Title != "Buy milk" {
		t.Errorf("list filter = %+v", work)
	}
}

func TestResolveByPrefix(t *testing.T) {
	o := ops(t, testapi.New())
	ctx := context.Background()
	if got, err := o.Resolve(ctx, "aaa111"); err != nil || got.Title != "Pay the cable bill" {
		t.Errorf("full id: %+v %v", got, err)
	}
	if got, err := o.Resolve(ctx, "bbb"); err != nil || got.ID != "bbb333" {
		t.Errorf("unique prefix (even of a completed todo): %+v %v", got, err)
	}
	_, err := o.Resolve(ctx, "aaa")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != codeAmbiguous || len(oe.Candidates) != 2 {
		t.Fatalf("an ambiguous prefix should name its candidates: %v", err)
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("ambiguous exit = %d, want usage", ExitCode(err))
	}
	if _, err := o.Resolve(ctx, "zzz"); ExitCode(err) != ExitNotFound {
		t.Errorf("no match should be not found, got %v", err)
	}
	if _, err := o.Resolve(ctx, ""); ExitCode(err) != ExitUsage {
		t.Errorf("empty reference should be a usage error")
	}
}

func TestSetDoneIsIdempotent(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	ctx := context.Background()
	c, err := o.SetDone(ctx, "aaa111", true)
	if err != nil || !c.Changed || !c.Todo.IsCompleted {
		t.Fatalf("first completion: %+v %v", c, err)
	}
	c, err = o.SetDone(ctx, "aaa111", true)
	if err != nil || c.Changed {
		t.Errorf("completing again should succeed and report no change: %+v %v", c, err)
	}
	c, err = o.SetDone(ctx, "aaa111", false)
	if err != nil || !c.Changed || c.Todo.IsCompleted {
		t.Errorf("reopening: %+v %v", c, err)
	}
}

func TestEditBuildsOneMinimalPatch(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	ctx := context.Background()
	title, day, none := "New title", "2026-04-01", "none"
	c, err := o.Edit(ctx, "aaa222", EditInput{Title: &title, Day: &day, Notes: &none})
	if err != nil || c.Todo.Title != "New title" {
		t.Fatalf("edit: %+v %v", c, err)
	}
	p := f.Patches[len(f.Patches)-1]
	if string(p["scheduledDay"]) != `"2026-04-01"` || string(p["notes"]) != "null" || string(p["title"]) != `"New title"` {
		t.Errorf("patch = %v", p)
	}
	if _, touched := p["dueAt"]; touched {
		t.Error("fields not asked for must not be sent")
	}
	if _, err := o.Edit(ctx, "aaa222", EditInput{}); ExitCode(err) != ExitUsage {
		t.Errorf("an empty edit should be a usage error")
	}
	empty := " "
	if _, err := o.Edit(ctx, "aaa222", EditInput{Title: &empty}); ExitCode(err) != ExitUsage {
		t.Errorf("an empty title should be refused")
	}
}

func TestEditReadsWordsThroughTheServer(t *testing.T) {
	f := testapi.New()
	at := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC) // 17:00 in Madrid
	f.Parsed = api.ParsePreview{DoAt: &at, HasTime: true}
	o := ops(t, f)
	when := "friday 5pm"
	if _, err := o.Edit(context.Background(), "aaa222", EditInput{At: &when}); err != nil {
		t.Fatal(err)
	}
	p := f.Patches[len(f.Patches)-1]
	if string(p["doAt"]) != `"2026-03-13T17:00:00+01:00"` {
		t.Errorf("doAt = %s, want the time in the account timezone", p["doAt"])
	}

	f.Parsed = api.ParsePreview{}
	gibberish := "blorp"
	_, err := o.Edit(context.Background(), "aaa222", EditInput{At: &gibberish})
	if ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "blorp") {
		t.Errorf("unreadable words should be a usage error naming them: %v", err)
	}
}

// The service answers "friday" with a do date at midnight and hasTime false.
// Every field takes that as the day, the way it takes 2026-03-13.
func TestEditReadsABareDayInWordsForEveryField(t *testing.T) {
	midnight := time.Date(2026, 3, 12, 23, 0, 0, 0, time.UTC) // 00:00 on the 13th in Madrid
	for _, words := range []string{"friday", "2026-03-13"} {
		f := testapi.New()
		f.Parsed = api.ParsePreview{DoAt: &midnight}
		o := ops(t, f)
		for field, want := range map[string]map[string]string{
			"day":    {"scheduledDay": `"2026-03-13"`},
			"at":     {"doAt": `"2026-03-13T00:00:00+01:00"`, "hasTime": "false"},
			"due":    {"dueAt": `"2026-03-13T00:00:00+01:00"`},
			"remind": {"remindAt": `"2026-03-13T00:00:00+01:00"`},
		} {
			in := EditInput{}
			switch field {
			case "day":
				in.Day = &words
			case "at":
				in.At = &words
			case "due":
				in.Due = &words
			case "remind":
				in.Remind = &words
			}
			if _, err := o.Edit(context.Background(), "aaa222", in); err != nil {
				t.Errorf("%s %q: %v", field, words, err)
				continue
			}
			p := f.Patches[len(f.Patches)-1]
			if len(p) != len(want) {
				t.Errorf("%s %q: patch = %v, want only %v", field, words, p, want)
			}
			for k, v := range want {
				if string(p[k]) != v {
					t.Errorf("%s %q: %s = %s, want %s", field, words, k, p[k], v)
				}
			}
		}
	}
}

func TestEditRefusesWordsWithNoDateForEveryField(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	words := "blorp"
	for field, in := range map[string]EditInput{
		"day": {Day: &words}, "at": {At: &words}, "due": {Due: &words}, "remind": {Remind: &words},
	} {
		_, err := o.Edit(context.Background(), "aaa222", in)
		if ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "blorp") {
			t.Errorf("%s: %v, want a usage error naming the words", field, err)
		}
	}
	if len(f.Patches) != 0 {
		t.Errorf("sent %d PATCHes, want none", len(f.Patches))
	}
}

func TestEditKeepsWallTimeInANonUTCAccount(t *testing.T) {
	f := testapi.New()                                  // the account is in Europe/Madrid
	at := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC) // 17:00 in Madrid, on summer time
	f.Parsed = api.ParsePreview{DoAt: &at, HasTime: true}
	o := ops(t, f)
	when := "tomorrow 5pm"
	if _, err := o.Edit(context.Background(), "aaa222", EditInput{At: &when}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Patches[len(f.Patches)-1]["doAt"]); got != `"2026-10-01T17:00:00+02:00"` {
		t.Errorf("doAt = %s, want 5pm to stay 17:00 on the account's clock", got)
	}
}

func TestEditStopsWhenTheAccountTimezoneIsUnknown(t *testing.T) {
	at := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		set  func(*testapi.API)
		exit int
	}{
		{"me fails", func(f *testapi.API) { f.MeStatus = 500 }, ExitServer},
		{"invalid timezone", func(f *testapi.API) { f.TZ = "Mars/Olympus" }, ExitUsage},
		{"local is not the account's clock", func(f *testapi.API) { f.TZ = "Local" }, ExitUsage},
	}
	for _, c := range cases {
		f := testapi.New()
		f.Parsed = api.ParsePreview{DoAt: &at, HasTime: true}
		c.set(f)
		o := ops(t, f)
		for _, field := range []string{"at", "due", "remind"} {
			when := "tomorrow 5pm"
			in := EditInput{}
			switch field {
			case "at":
				in.At = &when
			case "due":
				in.Due = &when
			case "remind":
				in.Remind = &when
			}
			title := "also changes"
			in.Title = &title
			_, err := o.Edit(context.Background(), "aaa222", in)
			if ExitCode(err) != c.exit || !strings.Contains(err.Error(), "timezone") {
				t.Errorf("%s, %s: exit %d (%v), want %d and a timezone error", c.name, field, ExitCode(err), err, c.exit)
			}
		}
		if len(f.Patches) != 0 {
			t.Errorf("%s: sent %d PATCHes, want none", c.name, len(f.Patches))
		}
	}
}

func TestEditNeedsNoTimezoneWhenTheValueIsNotReadInOne(t *testing.T) {
	f := testapi.New()
	f.MeStatus = 500
	day := "2026-03-14"
	f.Parsed = api.ParsePreview{ScheduledDay: &day}
	o := ops(t, f)
	ctx := context.Background()
	zoned, offset, words := "2026-03-10T15:00:00Z", "2026-03-10T15:00:00-05:00", "saturday"
	if _, err := o.Edit(ctx, "aaa222", EditInput{At: &zoned, Due: &offset}); err != nil {
		t.Fatal(err)
	}
	p := f.Patches[len(f.Patches)-1]
	if string(p["doAt"]) != `"2026-03-10T15:00:00Z"` || string(p["dueAt"]) != `"2026-03-10T15:00:00-05:00"` {
		t.Errorf("values with their own offset must be sent as given: %v", p)
	}
	// A day given with a time is only the day, whatever its offset.
	dayAt := "2026-03-10T23:30:00-05:00"
	if _, err := o.Edit(ctx, "aaa222", EditInput{Day: &dayAt}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Patches[len(f.Patches)-1]["scheduledDay"]); got != `"2026-03-10"` {
		t.Errorf("scheduledDay = %s, want the date alone", got)
	}
	local := "2026-03-10T15:00"
	if _, err := o.Edit(ctx, "aaa222", EditInput{Due: &local}); ExitCode(err) != ExitServer || !strings.Contains(err.Error(), "timezone") {
		t.Errorf("a time with no offset needs the account timezone: %v", err)
	}
	if _, err := o.Edit(ctx, "aaa222", EditInput{Day: &words}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Patches[len(f.Patches)-1]["scheduledDay"]); got != `"2026-03-14"` {
		t.Errorf("a calendar day from the parser needs no timezone: %s", got)
	}
	title := "retitled"
	if _, err := o.Edit(ctx, "aaa222", EditInput{Title: &title}); err != nil {
		t.Errorf("an edit with no dates never needed the account: %v", err)
	}
}

// The service refuses a date and time with no offset, and the fake does the
// same, so these fail if the client ever sends one.
func TestEditWritesOffsetsOnTheAccountClock(t *testing.T) {
	f := testapi.New() // Europe/Madrid, which changes to summer time on 2026-03-29
	o := ops(t, f)
	ctx := context.Background()
	cases := []struct{ in, want string }{
		{"2026-03-28T15:00", "2026-03-28T15:00:00+01:00"},
		{"2026-03-30T15:00", "2026-03-30T15:00:00+02:00"},
		{"2026-03-30T15:00:00Z", "2026-03-30T15:00:00Z"},
		{"2026-03-30T15:00:00+05:30", "2026-03-30T15:00:00+05:30"},
	}
	for _, c := range cases {
		in := c.in
		if _, err := o.Edit(ctx, "aaa222", EditInput{At: &in, Due: &in, Remind: &in}); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		p := f.Patches[len(f.Patches)-1]
		for _, k := range []string{"doAt", "dueAt", "remindAt"} {
			if string(p[k]) != `"`+c.want+`"` {
				t.Errorf("%s: %s = %s, want %s", c.in, k, p[k], c.want)
			}
		}
		if string(p["hasTime"]) != "true" {
			t.Errorf("%s: hasTime = %s, want true", c.in, p["hasTime"])
		}
	}
}

func TestEditDateOnlyDoAtSaysHasTimeFalse(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	day := "2026-03-10"
	if _, err := o.Edit(context.Background(), "aaa222", EditInput{At: &day, Due: &day}); err != nil {
		t.Fatal(err)
	}
	p := f.Patches[len(f.Patches)-1]
	if string(p["doAt"]) != `"2026-03-10T00:00:00+01:00"` || string(p["hasTime"]) != "false" {
		t.Errorf("doAt = %s hasTime = %s, want midnight with hasTime false", p["doAt"], p["hasTime"])
	}
	if string(p["dueAt"]) != `"2026-03-10T00:00:00+01:00"` {
		t.Errorf("dueAt = %s", p["dueAt"])
	}
}

func TestEditAssignAndMove(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	ctx := context.Background()
	who, anyone, work, nobody := "SAM@example.com", "anyone", "Work", "ghost@example.com"
	if _, err := o.Edit(ctx, "aaa222", EditInput{Assign: &who}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Patches[len(f.Patches)-1]["assigneeId"]); got != `"u2"` {
		t.Errorf("assigneeId = %s, want u2", got)
	}
	if _, err := o.Edit(ctx, "aaa222", EditInput{Assign: &anyone}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Patches[len(f.Patches)-1]["assigneeId"]); got != "null" {
		t.Errorf("assigneeId = %s, want null", got)
	}
	if _, err := o.Edit(ctx, "aaa222", EditInput{Assign: &nobody}); ExitCode(err) != ExitNotFound {
		t.Errorf("a non-member should be not found: %v", err)
	}
	if _, err := o.Edit(ctx, "aaa222", EditInput{List: &work}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Patches[len(f.Patches)-1]["listId"]); got != `"l2"` {
		t.Errorf("listId = %s, want l2", got)
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	ctx := context.Background()
	t1, err := o.Delete(ctx, "aaa111", false)
	if err == nil || ExitCode(err) != ExitUsage || t1.Title != "Pay the cable bill" {
		t.Fatalf("unconfirmed delete: %+v %v", t1, err)
	}
	if !strings.Contains(err.Error(), "Pay the cable bill") {
		t.Errorf("the message should say what would go: %v", err)
	}
	if f.Find("aaa111") == nil {
		t.Fatal("nothing should be deleted without confirmation")
	}
	if _, err := o.Delete(ctx, "aaa111", true); err != nil {
		t.Fatal(err)
	}
	if f.Find("aaa111") != nil {
		t.Error("confirmed delete should remove it")
	}
}

func TestShowAndSteps(t *testing.T) {
	f := testapi.New()
	o := ops(t, f)
	ctx := context.Background()
	if _, err := o.AddStep(ctx, "aaa111", "call them"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddStep(ctx, "aaa111", " "); ExitCode(err) != ExitUsage {
		t.Errorf("an empty step is a usage error")
	}
	d, err := o.Show(ctx, "aaa111")
	if err != nil || len(d.Steps) != 1 || d.ListName != "Personal" {
		t.Errorf("show = %+v %v", d, err)
	}
}

func TestParseRequiresText(t *testing.T) {
	o := ops(t, testapi.New())
	if _, err := o.Parse(context.Background(), ""); ExitCode(err) != ExitUsage {
		t.Errorf("empty parse should be a usage error")
	}
}

func TestExitCodesForAPIErrors(t *testing.T) {
	cases := []struct {
		err  *api.Error
		want int
	}{
		{&api.Error{StatusCode: 401, Code: api.CodeUnauthorized}, ExitAuth},
		{&api.Error{StatusCode: 403, Code: api.CodeSessionRequired}, ExitForbidden},
		{&api.Error{StatusCode: 403, Code: api.CodeListReadOnly}, ExitRejected},
		{&api.Error{StatusCode: 404, Code: api.CodeNotFound}, ExitNotFound},
		{&api.Error{StatusCode: 409, Code: api.CodeConflict}, ExitRejected},
		{&api.Error{StatusCode: 422, Code: api.CodeInvalidRequest}, ExitRejected},
		{&api.Error{StatusCode: 429, Code: api.CodeRateLimited, RetryAfter: 30 * time.Second}, ExitRateLimited},
		{&api.Error{StatusCode: 503, Code: api.CodeUpstreamFailed}, ExitServer},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("%d %s: exit %d, want %d", c.err.StatusCode, c.err.Code, got, c.want)
		}
	}
	info := Describe(&api.Error{StatusCode: 429, Code: api.CodeRateLimited, RetryAfter: 30 * time.Second, RequestID: "r1"})
	if !info.Retryable || info.RetryAfter != 30 || info.RequestID != "r1" {
		t.Errorf("info = %+v", info)
	}
	if ExitCode(nil) != ExitOK || ExitCode(errors.New("boom")) != ExitFailure {
		t.Error("nil is ok and unknown errors are a plain failure")
	}
}

func TestAPIFailurePassesThrough(t *testing.T) {
	f := testapi.New()
	f.FailWith = &api.Error{StatusCode: 401, Code: api.CodeUnauthorized}
	o := ops(t, f)
	_, err := o.ListTodos(context.Background(), ListInput{})
	if ExitCode(err) != ExitAuth {
		t.Errorf("exit = %d, want auth", ExitCode(err))
	}
}

func TestNetworkFailure(t *testing.T) {
	o := New(api.New("evt_x", api.WithBaseURL("http://127.0.0.1:1")))
	_, err := o.ListTodos(context.Background(), ListInput{})
	if ExitCode(err) != ExitNetwork {
		t.Errorf("exit = %d (%v), want network", ExitCode(err), err)
	}
}

// crowded fills the fake with n todos whose ids are t0000, t0001, ... so a
// listing of them cannot fit in one response.
func crowded(f *testapi.API, n int) {
	f.Todos = nil
	for i := range n {
		f.Todos = append(f.Todos, api.Todo{ID: fmt.Sprintf("t%04d", i), ListID: "l1", Title: fmt.Sprintf("todo %d", i), IsCompleted: i%2 == 0})
	}
}

func TestResolveFindsAPrefixBeyondTheFirstPage(t *testing.T) {
	f := testapi.New()
	crowded(f, 450)
	f.Todos[430].ID = "beyond-the-first-page"
	o := ops(t, f)
	got, err := o.Resolve(context.Background(), "beyond")
	if err != nil || got.ID != "beyond-the-first-page" {
		t.Fatalf("a match past the first page: %+v %v", got, err)
	}
	if _, err := o.Resolve(context.Background(), "nothing-like-it"); ExitCode(err) != ExitNotFound {
		t.Errorf("a prefix no todo has, in a complete lookup, is not found: %v", err)
	}
}

func TestResolveRejectsAPrefixSplitAcrossPages(t *testing.T) {
	f := testapi.New()
	crowded(f, 450)
	f.Todos[5].ID, f.Todos[430].ID = "dup-early", "dup-late"
	o := ops(t, f)
	_, err := o.Resolve(context.Background(), "dup")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != codeAmbiguous || len(oe.Candidates) != 2 {
		t.Fatalf("a prefix matching todos on different pages is ambiguous: %v", err)
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit = %d, want usage", ExitCode(err))
	}
}

func TestResolveUsesOneListingForManyReferences(t *testing.T) {
	f := testapi.New()
	crowded(f, 450) // three pages of the change feed
	o := ops(t, f)
	got, err := o.ResolveAll(context.Background(), []string{"t0001", "t0300", "t0449", "t04", "t0200"})
	if err == nil {
		t.Fatalf("t04 starts ten todos and should have failed: %+v", got)
	}
	if f.FeedRequests != 3 {
		t.Errorf("feed pulls = %d, want one listing of 3 pages shared by every reference", f.FeedRequests)
	}
	f.FeedRequests = 0
	got, err = o.ResolveAll(context.Background(), []string{"t0001", "t0300", "t0449", "t0200"})
	if err != nil || len(got) != 4 || got[1].ID != "t0300" {
		t.Fatalf("resolve all: %+v %v", got, err)
	}
	if f.FeedRequests != 0 {
		t.Errorf("full ids need no listing, got %d pulls", f.FeedRequests)
	}
}

func TestResolveIgnoresDeletedTodos(t *testing.T) {
	f := testapi.New()
	gone := time.Now()
	f.Todos = append(f.Todos, api.Todo{ID: "ccc444", ListID: "l1", Title: "Deleted", DeletedAt: &gone})
	o := ops(t, f)
	if _, err := o.Resolve(context.Background(), "ccc"); ExitCode(err) != ExitNotFound {
		t.Errorf("a deleted todo is not there to be resolved: %v", err)
	}
	f.Todos = append(f.Todos, api.Todo{ID: "ccc555", ListID: "l1", Title: "Kept"})
	if got, err := o.Resolve(context.Background(), "ccc"); err != nil || got.ID != "ccc555" {
		t.Errorf("a deleted todo must not make the prefix ambiguous: %+v %v", got, err)
	}
}

// feedServer answers every todo lookup with the given change-feed page, so a
// test can hand the resolver a listing that never ends or a server that
// cannot list at all.
func feedServer(t *testing.T, status int, page map[string]any) *Ops {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/todos/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found","code":"not_found"}`))
			return
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	return New(api.New("evt_test", api.WithBaseURL(srv.URL)))
}

func TestResolveRefusesAShortIDItCannotCheckAgainstEverything(t *testing.T) {
	one := []map[string]any{{"id": "abc123", "listId": "l1", "title": "Only one we saw"}}
	cases := map[string]*Ops{
		"a cursor that does not advance": feedServer(t, 200, map[string]any{"todos": one, "cursor": "5", "hasMore": true}),
		"more pages and no cursor":       feedServer(t, 200, map[string]any{"todos": one, "hasMore": true}),
		"a server with no feed":          feedServer(t, 400, map[string]any{"error": "bad_request", "code": "bad_request"}),
		// The status still says what happened when the body has no code.
		"a server with no feed and no code": feedServer(t, 400, map[string]any{"error": "Bad request"}),
	}
	for name, o := range cases {
		_, err := o.Resolve(context.Background(), "abc")
		var oe *Error
		if !errors.As(err, &oe) || oe.Code != codeIncomplete || ExitCode(err) != ExitUsage {
			t.Errorf("%s: want an incomplete-lookup usage error, got %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "full id") {
			t.Errorf("%s: the error should ask for the full id: %v", name, err)
		}
	}
	// The exact id still works without a listing.
	f := testapi.New()
	if got, err := ops(t, f).Resolve(context.Background(), "aaa111"); err != nil || got.ID != "aaa111" {
		t.Errorf("full id: %+v %v", got, err)
	}
}

func TestResolveKeepsServerFailuresApart(t *testing.T) {
	o := feedServer(t, 503, map[string]any{"error": "upstream_failed", "code": "upstream_failed"})
	if _, err := o.Resolve(context.Background(), "abc"); ExitCode(err) != ExitServer {
		t.Errorf("a server failure while listing keeps its own exit code: %v", err)
	}
}

// ops returns an Ops talking to a running fake.
func ops(t *testing.T, f *testapi.API) *Ops {
	t.Helper()
	return New(f.Client(t))
}

func TestChangesRefusesAFeedItCannotFollow(t *testing.T) {
	one := []map[string]any{{"id": "abc123", "listId": "l1", "title": "Only one we saw"}}
	cases := map[string]*Ops{
		"a cursor that does not advance": feedServer(t, 200, map[string]any{"todos": one, "cursor": "100", "hasMore": true}),
		"no cursor at all":               feedServer(t, 200, map[string]any{"todos": one, "hasMore": false}),
	}
	for name, o := range cases {
		got, err := o.Changes(context.Background(), ChangesInput{Since: "100"})
		var oe *Error
		if !errors.As(err, &oe) || oe.Code != codeFeedStalled || ExitCode(err) != ExitServer {
			t.Errorf("%s: want a stalled-feed server error, got %v", name, err)
		}
		if got.Complete || len(got.Todos) != 0 {
			t.Errorf("%s: a feed that cannot be followed must not return a result: %+v", name, got)
		}
	}
}

func TestChangesIncompleteResultHasItsOwnExitCode(t *testing.T) {
	f := testapi.New()
	f.FeedPageSize = 1
	got, err := ops(t, f).Changes(context.Background(), ChangesInput{Since: testapi.Cursor(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), MaxPages: 2})
	if err != nil || got.Complete || !got.HasMore || got.Pages != 2 || len(got.Todos) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if ExitCode(got.Err()) != ExitIncomplete || ExitIncomplete != 10 {
		t.Errorf("exit = %d, want 10", ExitCode(got.Err()))
	}
	if got.Cursor == "" || !strings.Contains(got.Err().Error(), got.Cursor) {
		t.Errorf("the error should give the resume cursor: %v", got.Err())
	}
	if (Changes{Complete: true}).Err() != nil {
		t.Error("a complete result has no error")
	}
}
