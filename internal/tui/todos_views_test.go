package tui

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
)

// A saved view is only a filter with a name, so most of what these tests
// pin down is what happens around it: that every part survives a restart,
// that it belongs to one account, and that it never opens narrower or
// wider than it was saved.

// viewsAPI is the sample account with people on its lists and todos to
// filter, since a view is nothing without something to tell apart.
func viewsAPI() *fakeAPI {
	f := sampleAPI()
	sam := "u2"
	f.todos[1].AssigneeID = &sam // Renew passport, in Household
	f.membersBy = map[string][]api.Member{
		"l1": {{UserID: "u1", Email: "demo@example.com", Role: "owner"}},
		"l2": {
			{UserID: "u1", Email: "demo@example.com", Role: "owner"},
			{UserID: "u2", Email: "sam@example.com", Role: "member"},
		},
		"l3": {{UserID: "u1", Email: "demo@example.com", Role: "member"}},
	}
	return f
}

// viewsHarness starts against the fake with a settings file of its own, so
// nothing is left over from another test.
func viewsHarness(t *testing.T, f *fakeAPI) *harness {
	t.Helper()
	t.Setenv("EVADO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	return newHarness(t, f)
}

// restartDirect is a new run that reads back what the last one wrote to
// the settings file, against the same server.
func (h *harness) restartDirect() *harness {
	h.t.Helper()
	run := h.model.cfg
	disk, err := config.Load()
	if err != nil {
		h.t.Fatal(err)
	}
	disk.Token, disk.BaseURL, disk.NoCache = run.Token, run.BaseURL, true
	m := New(context.Background(), disk)
	m.ready = true
	again := &harness{t: h.t, model: m}
	again.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	again.run(m.Init())
	return again
}

// saveAs saves the filters on screen under a name, the way a person does.
func (h *harness) saveAs(name string) {
	h.t.Helper()
	h.press("s")
	h.typeText(name)
	h.press("enter")
}

// show puts a filter on screen, as choosing it would.
func (h *harness) show(f todoFilter) {
	h.t.Helper()
	h.run(h.model.todos.choose(h.model, f))
}

// open opens a saved view by name, as the sidebar and the palette do.
func (h *harness) open(name string) {
	h.t.Helper()
	h.run(h.model.todos.openSaved(h.model, name))
}

// views are the saved views of whoever is on screen.
func (h *harness) views() []config.SavedView { return h.model.todos.savedViews() }

func viewNames(views []config.SavedView) []string {
	names := make([]string, len(views))
	for i, sv := range views {
		names[i] = sv.Name
	}
	return names
}

// onDisk reads the saved views of the scope on screen out of the file, not
// out of the running model.
func (h *harness) onDisk() []config.SavedView {
	h.t.Helper()
	disk, err := config.Load()
	if err != nil {
		h.t.Fatal(err)
	}
	scope, ok := h.model.viewScope()
	if !ok {
		h.t.Fatal("no account is known yet")
	}
	return disk.ViewsFor(scope)
}

// seed stores views for the account on screen, as an earlier run left them.
func (h *harness) seed(views ...config.SavedView) {
	h.t.Helper()
	scope, ok := h.model.viewScope()
	if !ok {
		h.t.Fatal("no account is known yet")
	}
	if err := h.model.todos.keepViews(scope, views); err != nil {
		h.t.Fatal(err)
	}
}

// titles are the todos on screen, in one string.
func (h *harness) titles() string {
	var out []string
	for _, td := range h.model.todos.items {
		out = append(out, td.Title)
	}
	return strings.Join(out, ",")
}

// lastQuery is the newest filtered request the server saw.
func lastQuery(f *fakeAPI) url.Values {
	if len(f.queries) == 0 {
		return url.Values{}
	}
	return f.queries[len(f.queries)-1]
}

func TestEveryFilterIsKeptAcrossARestart(t *testing.T) {
	cases := []struct {
		name string
		f    todoFilter
	}{
		{"everything", todoFilter{mode: viewEverything, status: api.StatusToday}},
		{"today", todoFilter{mode: viewStatus, status: api.StatusToday}},
		{"overdue in a list", todoFilter{mode: viewStatus, status: api.StatusOverdue, listID: "l1"}},
		{"upcoming as a board", todoFilter{mode: viewStatus, status: api.StatusUpcoming, board: true}},
		{"completed and undated", todoFilter{mode: viewStatus, status: api.StatusCompleted, undated: true}},
		{"mine", todoFilter{mode: viewEverything, status: api.StatusToday, assigned: "me"}},
		{"unassigned", todoFilter{mode: viewEverything, status: api.StatusToday, assigned: "anyone"}},
		{"one member", todoFilter{mode: viewEverything, status: api.StatusToday, assigned: "u2", listID: "l2"}},
		{"a member across lists", todoFilter{mode: viewEverything, status: api.StatusToday, assigned: "u2"}},
		{"a search", todoFilter{mode: viewEverything, status: api.StatusToday, query: "passport"}},
		{"undated", todoFilter{mode: viewEverything, status: api.StatusToday, undated: true}},
		{"all of it", todoFilter{
			mode: viewStatus, status: api.StatusToday, listID: "l2", assigned: "u2",
			query: "pass", board: true,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := viewsHarness(t, viewsAPI())
			h.show(c.f)
			h.saveAs("Mine")

			again := h.restartDirect()
			if got := viewNames(again.views()); len(got) != 1 || got[0] != "Mine" {
				t.Fatalf("views after a restart = %v", got)
			}
			// What the tab reopens on is not the view: only a few parts of the
			// last filter are remembered, so opening it has to bring the rest.
			again.open("Mine")
			if !again.model.todos.filter.is(c.f) {
				t.Errorf("opened %+v, want the whole combination %+v", again.model.todos.filter, c.f)
			}
			if again.model.todos.repair != nil {
				t.Errorf("a complete view opened in the repair state: %+v", again.model.todos.repair.issues)
			}
		})
	}
}

func TestSavingAsksForANameAndKeepsTheFilters(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday, listID: "l2", assigned: "me"})
	h.press("s")
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want the name prompt", h.model.layer)
	}
	h.mustContain("Save this view")
	h.mustContain("Today only · Household · assigned to me")
	h.typeText("Home today")
	h.press("enter")

	want := config.SavedView{
		Name: "Home today", Status: api.StatusToday, ListID: "l2", ListName: "Household", Assigned: "me",
	}
	got := h.onDisk()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("on disk = %+v, want %+v", got, want)
	}
	h.mustContain(`saved view "Home today"`)
	h.mustContain("Home today · Today only · Household · assigned to me")
}

func TestASavedViewNamesItsPersonAndList(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.show(todoFilter{mode: viewEverything, status: api.StatusToday, listID: "l2", assigned: "u2"})
	h.saveAs("With Sam")
	got := h.onDisk()
	if len(got) != 1 || got[0].ListName != "Household" || got[0].AssignedName == "" {
		t.Errorf("saved %+v, want the list and the person named for a later repair", got)
	}
}

func TestSavedViewNamesAreChecked(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.saveAs("Work")
	h.saveAs("work ")
	if h.model.layer != layerForm {
		t.Fatal("a name that is taken should bring the prompt back")
	}
	h.mustContain("already a view called")
	h.press("esc")
	h.press("s", "enter")
	h.mustContain("needs a name")
	h.press("esc")
	h.saveAs(strings.Repeat("x", maxViewName+1))
	h.mustContain("at most")
	h.press("esc")
	if got := viewNames(h.views()); len(got) != 1 {
		t.Errorf("views = %v, want only the first", got)
	}
}

// sidebarView moves the sidebar's cursor onto a saved view and opens it.
func (h *harness) sidebarView(name string) {
	h.t.Helper()
	h.press("h")
	for i := 0; i < 40; i++ {
		items := h.model.todos.sideItems()
		if it := items[h.model.todos.side]; it.isSaved && it.label == name {
			h.press("enter")
			return
		}
		h.press("j")
	}
	h.t.Fatalf("no saved view %q in the sidebar", name)
}

func TestSavedViewsOpenFromTheSidebar(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	want := todoFilter{mode: viewStatus, status: api.StatusCompleted, listID: "l1", board: true}
	h.show(want)
	h.saveAs("Finished")
	h.show(defaultFilter())
	h.mustContain("Finished")

	h.sidebarView("Finished")
	if !h.model.todos.filter.is(want) {
		t.Errorf("filter = %+v, want %+v", h.model.todos.filter, want)
	}
	if h.model.focus != focusList {
		t.Errorf("focus = %v, want the list once a view is opened", h.model.focus)
	}
	h.mustContain("Finished · Board · Completed only · Personal")
	h.mustContain(`opened "Finished"`)
}

func TestSavedViewsOpenFromTheCommandPalette(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	want := todoFilter{mode: viewEverything, status: api.StatusToday, query: "passport"}
	h.show(want)
	h.saveAs("Passport hunt")
	h.show(defaultFilter())

	h.press("2") // from another tab, to show the palette brings the tab too
	h.press(":")
	h.typeText("open passport")
	h.mustContain("Views")
	h.press("enter")
	if h.model.tab != tabTodos || !h.model.todos.filter.is(want) {
		t.Errorf("tab %v filter %+v, want the view opened on Todos", h.model.tab, h.model.todos.filter)
	}
}

func TestThePaletteListsTheNewActions(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.press(":")
	h.typeText("views")
	for _, want := range []string{"save the filters", "rename, update or remove"} {
		h.mustContain(want)
	}
	h.press("esc", ":")
	h.typeText("undated")
	h.mustContain("no date")
	h.press("esc", ":")
	h.typeText("person")
	h.mustContain("one person")
}

func TestHelpListsTheNewKeys(t *testing.T) {
	var keys []string
	for _, s := range keyReference() {
		for _, e := range s.entries {
			keys = append(keys, e.keys)
		}
	}
	for _, want := range []string{"@", "U", "s", "S"} {
		found := false
		for _, k := range keys {
			found = found || k == want
		}
		if !found {
			t.Errorf("the help has no entry for %q", want)
		}
	}
}

func TestViewMenuRenamesUpdatesAndRemoves(t *testing.T) {
	f := viewsAPI()
	t.Setenv("EVADO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	// Settings that are not views, and the views of someone else, which
	// nothing here may touch.
	other := config.AccountViews("https://other.example/api/v1", "u9")
	seed := config.Config{Theme: "dark", PollSeconds: 300, Keys: map[string]string{"J": "j"}}
	seed.SetViews(other, []config.SavedView{{Name: "Not mine", Status: api.StatusToday}})
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, f, func(c *config.Config) { c.SavedViews = seed.SavedViews })

	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("First")
	h.show(todoFilter{mode: viewStatus, status: api.StatusUpcoming, listID: "l2"})
	h.saveAs("Second")

	// Rename: the menu starts on the view that is open.
	h.press("S", "tab", "right", "enter") // Second, Rename
	h.mustContain("Rename view")
	h.press("ctrl+u")
	h.typeText("Household soon")
	h.press("enter")
	if got := viewNames(h.onDisk()); strings.Join(got, ",") != "First,Household soon" {
		t.Fatalf("after the rename the views are %v", got)
	}
	h.mustContain("Household soon · Upcoming only · Household")

	// Update: the filters on screen replace what "First" held.
	h.show(todoFilter{mode: viewEverything, status: api.StatusToday, assigned: "me", board: true})
	h.press("S", "left", "tab", "right", "right", "enter") // First, Update
	first := h.onDisk()[0]
	if first.Name != "First" || first.Status != "" || first.Assigned != "me" || !first.Board {
		t.Errorf("First after the update = %+v, want the filters on screen", first)
	}
	if h.onDisk()[1].Status != api.StatusUpcoming {
		t.Errorf("updating one view changed the other: %+v", h.onDisk()[1])
	}

	// Remove asks first, and takes only the view.
	todos, deletes := len(f.todos), len(f.deletes)
	h.press("S", "tab", "right", "right", "right", "enter")
	if h.model.layer != layerConfirm {
		t.Fatalf("layer = %v, removing must ask first", h.model.layer)
	}
	h.press("esc")
	if len(h.onDisk()) != 2 {
		t.Fatal("declining the question removed the view")
	}
	h.press("S", "tab", "right", "right", "right", "enter", "enter")
	if got := viewNames(h.onDisk()); strings.Join(got, ",") != "Household soon" {
		t.Errorf("after the removal the views are %v", got)
	}
	if h.model.busy {
		t.Error("the confirmation left the busy state on")
	}
	if len(f.todos) != todos || len(f.deletes) != deletes {
		t.Error("removing a view touched todos")
	}
	disk, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if disk.Theme != "dark" || disk.PollSeconds != 300 || disk.Keys["J"] != "j" {
		t.Errorf("settings changed: %+v", disk)
	}
	if got := disk.ViewsFor(other); len(got) != 1 || got[0].Name != "Not mine" {
		t.Errorf("another account's views changed: %+v", got)
	}
	for _, it := range h.model.todos.sideItems() {
		if it.label == "First" {
			t.Error("the removed view is still in the sidebar")
		}
	}
}

func TestOpeningAViewFromTheMenu(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	want := todoFilter{mode: viewStatus, status: api.StatusOverdue}
	h.show(want)
	h.saveAs("Late")
	h.show(defaultFilter())
	h.press("S", "enter")
	if !h.model.todos.filter.is(want) {
		t.Errorf("filter = %+v, want %+v", h.model.todos.filter, want)
	}
}

func TestTheMenuSaysWhenThereAreNoViews(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.press("S")
	if h.model.layer != layerNone {
		t.Error("the menu opened with nothing in it")
	}
	h.mustContain("no saved views yet")
}

func TestAChangedFilterIsNoLongerTheView(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("Today")
	h.mustContain("Today · Today only")
	h.press("f") // another status
	h.mustNotContain("Today · Upcoming only")
	if _, ok := h.model.todos.activeView(); ok {
		t.Error("the list still claims a view it is no longer showing")
	}
}

// -- who the views belong to ------------------------------------------------

func TestViewsBelongToTheAccountTheyWereSavedFor(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("Mine")

	signIn := func(id string) {
		h.send(meMsg{seq: h.model.meSeq, user: api.User{ID: id, Email: id + "@example.com", TZ: "UTC"}, lists: f.lists})
		h.run(h.cmds...)
		h.cmds = nil
	}
	signIn("u2")
	if got := viewNames(h.views()); len(got) != 0 {
		t.Fatalf("another account sees %v", got)
	}
	for _, it := range h.model.todos.sideItems() {
		if it.isSaved {
			t.Errorf("the sidebar shows %q to another account", it.label)
		}
	}
	h.show(todoFilter{mode: viewStatus, status: api.StatusOverdue})
	h.saveAs("Theirs")
	if got := viewNames(h.views()); len(got) != 1 || got[0] != "Theirs" {
		t.Fatalf("views = %v", got)
	}

	signIn("u1")
	if got := viewNames(h.views()); len(got) != 1 || got[0] != "Mine" {
		t.Errorf("back on the first account the views are %v", got)
	}
	h.press(":")
	h.typeText("open")
	h.mustContain("open Mine")
	h.mustNotContain("open Theirs")
	h.press("esc")

	disk, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.SavedViews) != 2 {
		t.Fatalf("the file holds %d sets, want one for each account: %+v", len(disk.SavedViews), disk.SavedViews)
	}
	server := h.model.cfg.BaseURL
	if disk.SavedViews[0].Account != "u1" || disk.SavedViews[1].Account != "u2" ||
		disk.SavedViews[0].Server != server || disk.SavedViews[1].Server != server {
		t.Errorf("sets are keyed %+v, want the server and account of each", disk.SavedViews)
	}
}

func TestSwitchingAccountsLeavesNoOpenView(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday, listID: "gone"})
	h.saveAs("Broken")
	h.model.todos.repair = &repairState{view: h.views()[0]}
	h.send(meMsg{seq: h.model.meSeq, user: api.User{ID: "u2", TZ: "UTC"}, lists: f.lists})
	if h.model.todos.repair != nil || h.model.todos.opened != "" {
		t.Error("the last account's repair notice followed the switch")
	}
}

func TestTheSameAccountOnAnotherServerHasOtherViews(t *testing.T) {
	f := viewsAPI()
	t.Setenv("EVADO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	seed := config.Config{}
	seed.SetViews(config.AccountViews("https://elsewhere.example/api/v1", "u1"), []config.SavedView{{Name: "Elsewhere"}})
	h := newHarnessWith(t, f, func(c *config.Config) { c.SavedViews = seed.SavedViews })
	if got := viewNames(h.views()); len(got) != 0 {
		t.Errorf("a view from another server showed up: %v", got)
	}
}

func TestGuestsHaveViewsOfTheirOwn(t *testing.T) {
	acct := config.AccountViews(api.DefaultBaseURL, "u1")
	seed := config.Config{Local: true}
	seed.SetViews(acct, []config.SavedView{{Name: "Account only", Status: api.StatusToday}})
	h := offlineHarness(t, seed)
	h.mustNotContain("Account only")

	h.press("s")
	h.typeText("Guest view")
	h.press("enter")
	h.mustContain("Guest view")
	if got := viewNames(h.views()); len(got) != 1 || got[0] != "Guest view" {
		t.Fatalf("the guest's views are %v", got)
	}

	disk, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := disk.ViewsFor(acct); len(got) != 1 || got[0].Name != "Account only" {
		t.Errorf("the account's views changed: %+v", got)
	}
	if got := disk.ViewsFor(config.ViewScope{}); len(got) != 1 || got[0].Name != "Guest view" {
		t.Errorf("the guest's views on disk are %+v", got)
	}

	again := restart(t, disk)
	if got := viewNames(again.views()); len(got) != 1 || got[0] != "Guest view" {
		t.Errorf("after a restart the guest's views are %v", got)
	}
}

func TestSigningInLeavesTheGuestViewsBehind(t *testing.T) {
	acct, server := newAccount(t)
	_ = acct
	account := config.AccountViews(server, "u1")
	seed := config.Config{Local: true, BaseURL: server}
	seed.SetViews(account, []config.SavedView{{Name: "From the account", Status: api.StatusToday}})
	h := offlineHarness(t, seed)
	h.press("s")
	h.typeText("Guest view")
	h.press("enter")
	h.mustContain("Guest view")

	h.press("4")
	h.model.acct.selectRow("computer")
	h.press("c")
	h.typeText("evt_good")
	h.press("enter")
	h.press("1")

	if got := viewNames(h.views()); len(got) != 1 || got[0] != "From the account" {
		t.Errorf("after signing in the views are %v, want the account's and not the guest's", got)
	}
	h.mustNotContain("Guest view")
}

func TestNoViewsAreShownUntilTheAccountIsKnown(t *testing.T) {
	f := viewsAPI()
	t.Setenv("EVADO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	h := newHarness(t, f)
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("Mine")
	h.model.user = api.User{}
	if got := h.views(); len(got) != 0 {
		t.Errorf("views = %v before the account is read", got)
	}
	h.press("s")
	if h.model.layer == layerForm {
		t.Error("a view was offered to be saved with nobody to save it for")
	}
}

// -- what is saved, and what never is ---------------------------------------

func TestSavingViewsNeverWritesThePerRunToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("EVADO_CONFIG", path)
	if err := (config.Config{Token: "evt_on_disk", Theme: "dark"}).Save(); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, viewsAPI()) // runs with evt_test, as -token would
	if h.model.cfg.Token != "evt_test" {
		t.Fatalf("token = %q, want the per-run one", h.model.cfg.Token)
	}
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("One")
	h.show(todoFilter{mode: viewStatus, status: api.StatusOverdue})
	h.saveAs("Two")
	h.press("S", "tab", "right", "enter") // rename Two
	h.press("ctrl+u")
	h.typeText("Three")
	h.press("enter")
	h.press("S", "tab", "right", "right", "enter") // update
	h.press("S", "tab", "right", "right", "right", "enter", "enter")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "evt_test") {
		t.Errorf("the per-run token was written:\n%s", raw)
	}
	disk, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if disk.Token != "evt_on_disk" || disk.Theme != "dark" {
		t.Errorf("the file holds %+v, want the stored token and settings untouched", disk)
	}
	if len(h.onDisk()) != 1 {
		t.Errorf("views on disk = %+v, want the one that is left", h.onDisk())
	}
}

func TestAnUnreadableSettingsFileIsNotOverwrittenBySaving(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("EVADO_CONFIG", path)
	h := newHarness(t, viewsAPI())
	const broken = "{ not json, but the user's own"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("Kept for now")

	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != broken {
		t.Errorf("the file is now %q, want it left alone", raw)
	}
	if !strings.Contains(h.model.toastText(), "could not be written") {
		t.Errorf("toast = %q, want it to say the view was not saved for next time", h.model.toastText())
	}
	if got := viewNames(h.views()); len(got) != 1 {
		t.Errorf("views = %v, want the view kept for this run", got)
	}
}

// -- views that name what is gone -------------------------------------------

func TestAViewWithAMissingListIsNotOpenedWithoutIt(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{Name: "Old work", Status: api.StatusToday, ListID: "gone", ListName: "Archive"})
	before := h.model.todos.filter
	loads := len(f.queries)

	h.open("Old work")
	v := &h.model.todos
	if v.repair == nil || len(v.repair.issues) != 1 || v.repair.issues[0].kind != issueList {
		t.Fatalf("repair = %+v, want the missing list", v.repair)
	}
	if len(f.queries) != loads {
		t.Errorf("the server was asked %d times for a view that is not open", len(f.queries)-loads)
	}
	if v.filter != before {
		t.Errorf("the filter moved to %+v while the view needs repair", v.filter)
	}
	h.mustContain("Old work · needs repair")
	h.mustContain("“Archive” is no longer available")
	h.mustContain("not opened")

	// Keys that act on a todo are not let through to the ones hidden behind it.
	h.press("d", "space", "e")
	if h.model.layer != layerNone || len(f.deletes) != 0 || len(f.patches) != 0 {
		t.Error("a key reached a todo behind the repair notice")
	}
	// It stays flagged in the sidebar until it is mended.
	h.press("h")
	h.mustContain("Old work")
	h.mustContain("!")
}

func TestRepairingAListPicksAnotherAndKeepsIt(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{Name: "Old work", Status: api.StatusToday, ListID: "gone", ListName: "Archive", Board: true})
	h.open("Old work")

	h.press("enter")
	if h.model.layer != layerForm {
		t.Fatal("enter should offer the repair")
	}
	h.mustContain("Repair")
	h.mustContain("no longer available")
	h.press("right", "enter") // Household
	v := &h.model.todos
	if v.repair != nil {
		t.Fatalf("still needs repair: %+v", v.repair.issues)
	}
	want := todoFilter{mode: viewStatus, status: api.StatusToday, listID: "l2", board: true}
	if !v.filter.is(want) {
		t.Errorf("filter = %+v, want %+v", v.filter, want)
	}
	if got := lastQuery(f); got.Get("listId") != "l2" || got.Get("status") != "today" {
		t.Errorf("the server was asked %v", got)
	}
	got := h.onDisk()
	if len(got) != 1 || got[0].ListID != "l2" || got[0].ListName != "Household" || got[0].Status != api.StatusToday {
		t.Errorf("the repaired view is %+v", got)
	}
}

func TestRepairingByDroppingThePartIsAChoice(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{Name: "Old work", Status: api.StatusToday, ListID: "gone"})
	h.open("Old work")
	h.press("enter", "left", "enter") // the last choice is to drop the list
	if h.model.todos.repair != nil {
		t.Fatal("still needs repair after the list was dropped")
	}
	if got := lastQuery(f); got.Get("listId") != "" || got.Get("status") != "today" {
		t.Errorf("the server was asked %v, want today with no list", got)
	}
	if got := h.onDisk(); len(got) != 1 || got[0].ListID != "" || got[0].Status != api.StatusToday {
		t.Errorf("the view on disk is %+v", got)
	}
}

func TestLeavingARepairShowsTheOldListAgain(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.seed(config.SavedView{Name: "Old work", ListID: "gone"})
	h.open("Old work")
	h.press("esc")
	if h.model.todos.repair != nil {
		t.Error("esc should leave the repair state")
	}
	h.mustContain("Renew passport")
	if got := h.onDisk(); len(got) != 1 || got[0].ListID != "gone" {
		t.Errorf("leaving changed the view: %+v", got)
	}
}

func TestAViewWithAFormerMemberIsNotOpenedWithoutThem(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{
		Name: "Jo's", Status: api.StatusToday, ListID: "l2", ListName: "Household",
		Assigned: "u9", AssignedName: "@jo",
	})
	loads := len(f.queries)
	h.open("Jo's")

	v := &h.model.todos
	if v.repair == nil || v.repair.issues[0].kind != issuePerson {
		t.Fatalf("repair = %+v, want the former member", v.repair)
	}
	if len(f.queries) != loads {
		t.Error("the server was asked for todos assigned to someone who is gone")
	}
	h.mustContain("@jo is no longer a member of Household")

	h.press("enter")
	h.mustContain("person")
	h.press("enter") // the first person offered
	if v.repair != nil {
		t.Fatalf("still needs repair: %+v", v.repair.issues)
	}
	if got := lastQuery(f); got.Get("assigned") != "u2" || got.Get("listId") != "l2" {
		t.Errorf("the server was asked %v, want Sam's todos in Household", got)
	}
	if got := h.onDisk(); got[0].Assigned != "u2" || got[0].AssignedName != "sam@example.com" {
		t.Errorf("the repaired view is %+v", got[0])
	}
}

func TestAMemberWhoIsStillThereNeedsNoRepair(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{Name: "Sam's", ListID: "l2", Assigned: "u2"})
	h.open("Sam's")
	if h.model.todos.repair != nil {
		t.Fatalf("repair = %+v", h.model.todos.repair.issues)
	}
	if got := lastQuery(f); got.Get("assigned") != "u2" {
		t.Errorf("the server was asked %v", got)
	}
	h.mustContain("Renew passport")
}

func TestAMemberWhoLeftEveryListIsFoundOutAtOpenTime(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{Name: "Sam's", Assigned: "u2"})
	f.membersBy["l2"] = f.membersBy["l2"][:1] // Sam leaves after the last look
	h.open("Sam's")
	if h.model.todos.repair == nil {
		t.Fatal("a member who is gone was not noticed, so the view would have shown todos assigned to nobody")
	}
	h.mustContain("no longer a member of any of your lists")
}

func TestAViewIsOpenedWhenMembershipCannotBeChecked(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{Name: "Sam's", Assigned: "u2", ListID: "l2"})
	f.refuse = func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/members") }
	h.open("Sam's")
	// The person filter is still applied, so not knowing never widens it.
	if h.model.todos.repair != nil {
		t.Fatal("a view was held back because the members could not be read")
	}
	if got := lastQuery(f); got.Get("assigned") != "u2" {
		t.Errorf("the server was asked %v, want the person kept", got)
	}
}

func TestAListThatVanishesUnderAnOpenViewIsRepairedNotDropped(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday, listID: "l3"})
	h.saveAs("Work today")
	h.open("Work today")

	// The list is left elsewhere and the next refresh no longer has it.
	f.lists = f.lists[:2]
	loads := len(f.queries)
	h.send(meMsg{seq: h.model.meSeq, user: api.User{ID: "u1", TZ: "UTC"}, lists: f.lists})
	h.run(h.cmds...)
	h.cmds = nil

	v := &h.model.todos
	if v.repair == nil {
		t.Fatal("the view was not flagged")
	}
	if v.filter.listID != "l3" {
		t.Errorf("the list filter was dropped: %+v", v.filter)
	}
	if len(f.queries) != loads {
		t.Error("the list was reloaded without its list filter")
	}
	h.mustContain("needs repair")
}

func TestAListThatVanishesWithNoViewOpenIsStillDropped(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday, listID: "l3"})
	f.lists = f.lists[:2]
	h.send(meMsg{seq: h.model.meSeq, user: api.User{ID: "u1", TZ: "UTC"}, lists: f.lists})
	h.run(h.cmds...)
	h.cmds = nil
	if h.model.todos.filter.listID != "" {
		t.Error("a plain list filter whose list is gone should still clear, as it always has")
	}
}

func TestAnUnknownStatusIsRepairedNotIgnored(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.seed(config.SavedView{Name: "Someday", Status: "someday"})
	h.open("Someday")
	if h.model.todos.repair == nil || h.model.todos.repair.issues[0].kind != issueStatus {
		t.Fatalf("repair = %+v", h.model.todos.repair)
	}
	h.press("enter", "enter") // the first status offered
	if h.model.todos.repair != nil {
		t.Fatal("still needs repair")
	}
	if got := h.onDisk(); got[0].Status != api.StatusOverdue {
		t.Errorf("the repaired view is %+v", got[0])
	}
}

// -- relative statuses ------------------------------------------------------

func TestTodayIsWorkedOutInTheAccountTimezoneWhenTheViewOpens(t *testing.T) {
	f := viewsAPI()
	on10, on11 := "2026-03-10", "2026-03-11"
	f.todos = []api.Todo{
		{ID: "a", ListID: "l1", Title: "Due on the 10th", ScheduledDay: &on10},
		{ID: "b", ListID: "l1", Title: "Due on the 11th", ScheduledDay: &on11},
	}
	// Late evening of the 10th in UTC, which is already the 11th in
	// Auckland and still the 10th in Los Angeles.
	now := time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC)
	f.clock = func() time.Time { return now }
	h := viewsHarness(t, f)
	h.model.now = now

	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("Today")
	raw, err := os.ReadFile(os.Getenv("EVADO_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "2026") || !strings.Contains(string(raw), `"status": "today"`) {
		t.Errorf("the view stored a date, or not the word today:\n%s", raw)
	}

	showing := func() string {
		var titles []string
		for _, td := range h.model.todos.items {
			titles = append(titles, td.Title)
		}
		return strings.Join(titles, ",")
	}
	setZone := func(tz string) {
		f.tz = tz
		h.run(h.model.fetchMe())
	}

	h.open("Today")
	if got := showing(); got != "Due on the 10th" {
		t.Fatalf("in UTC the view shows %q", got)
	}

	setZone("Pacific/Auckland")
	h.open("Today")
	if got := showing(); got != "Due on the 11th" {
		t.Errorf("in Auckland the view shows %q, want the 11th", got)
	}
	if got := lastQuery(f); got.Get("status") != "today" {
		t.Errorf("the server was asked %v, want the status word and no date", got)
	}

	setZone("America/Los_Angeles")
	h.open("Today")
	if got := showing(); got != "Due on the 10th" {
		t.Errorf("in Los Angeles the view shows %q, want the 10th", got)
	}

	// The same view, opened by a later run after midnight, is another day.
	setZone("UTC")
	now = now.Add(time.Hour)
	again := h.restartDirect()
	again.model.now = now
	again.open("Today")
	var titles []string
	for _, td := range again.model.todos.items {
		titles = append(titles, td.Title)
	}
	if strings.Join(titles, ",") != "Due on the 11th" {
		t.Errorf("after midnight the view shows %v, want the 11th", titles)
	}
}

// -- the pickers ------------------------------------------------------------

func TestUndatedNarrowsToTodosWithNoDate(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.press("U")
	if !h.model.todos.filter.undated {
		t.Fatal("U should narrow to undated todos")
	}
	if got := lastQuery(f); got.Get("undated") != "1" {
		t.Errorf("the server was asked %v", got)
	}
	h.mustContain("undated")
	h.mustContain("Renew passport")
	h.mustNotContain("Pay the cable bill")
	h.press("U")
	if h.model.todos.filter.undated {
		t.Error("U again should turn it off")
	}
	h.mustContain("Pay the cable bill")
}

func TestUndatedLeavesADatedStatusForEverything(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.press("U")
	if h.model.todos.filter.mode != viewEverything || !h.model.todos.filter.undated {
		t.Errorf("filter = %+v, want Everything and undated: no dated status holds an undated todo", h.model.todos.filter)
	}
	h.show(todoFilter{mode: viewStatus, status: api.StatusCompleted})
	h.press("U")
	if h.model.todos.filter.mode != viewStatus || !h.model.todos.filter.undated {
		t.Errorf("filter = %+v, want completed undated todos", h.model.todos.filter)
	}
	// Stepping to a dated status lets go of it again.
	h.press("f") // Everything
	h.press("f") // Overdue
	if h.model.todos.filter.undated {
		t.Error("a dated status kept the undated filter")
	}
}

func TestPickingOneMemberFiltersToTheirTodos(t *testing.T) {
	f := viewsAPI()
	h := viewsHarness(t, f)
	h.press("@")
	if h.model.layer != layerForm {
		t.Fatal("@ should open the person picker")
	}
	h.mustContain("Show todos assigned to")
	h.mustContain("Everyone, no filter")
	h.press("right", "right", "right", "enter") // Me, Nobody, Sam
	if h.model.todos.filter.assigned != "u2" {
		t.Fatalf("assigned = %q, want Sam", h.model.todos.filter.assigned)
	}
	if got := lastQuery(f); got.Get("assigned") != "u2" {
		t.Errorf("the server was asked %v", got)
	}
	h.mustContain("assigned to sam@example.com")
	h.mustContain("Renew passport")
	h.mustNotContain("Pay the cable bill")

	// a from here goes to everyone, rather than getting stuck on Sam.
	h.press("a")
	if h.model.todos.filter.assigned != "" {
		t.Errorf("a left the filter on %q", h.model.todos.filter.assigned)
	}
}

func TestThePersonPickerOffersTheMembersOfTheListOnScreen(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.show(todoFilter{mode: viewEverything, status: api.StatusToday, listID: "l1"})
	h.press("@")
	h.mustContain("Nobody, unassigned todos")
	h.mustNotContain("sam@example.com") // Sam is not on Personal
	h.press("esc")
	h.show(todoFilter{mode: viewEverything, status: api.StatusToday, listID: "l2"})
	h.press("@")
	h.press("right", "right", "right")
	h.mustContain("sam@example.com")
}

// -- the local backend ------------------------------------------------------

func TestViewsWorkAgainstTheTodosKeptOnThisComputer(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	for _, title := range []string{"Buy milk", "Call Sam tomorrow"} {
		h.press("n")
		h.typeText(title)
		h.press("enter")
	}
	h.press("U")
	if got := h.titles(); got != "Buy milk" {
		t.Fatalf("undated shows %q", got)
	}
	h.press("s")
	h.typeText("Inbox only")
	h.press("enter")

	disk, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	again := restart(t, disk)
	again.open("Inbox only")
	again.mustContain("Inbox only · Everything · undated")
	if got := again.titles(); got != "Buy milk" {
		t.Errorf("the view shows %q, want only what has no date", got)
	}
	// The freshness cue is the same one the rest of the tab carries.
	again.mustContain("on this computer")
}

func TestTheMenuStartsOnTheViewUnderTheSidebarCursor(t *testing.T) {
	h := viewsHarness(t, viewsAPI())
	h.show(todoFilter{mode: viewStatus, status: api.StatusToday})
	h.saveAs("First")
	h.show(todoFilter{mode: viewStatus, status: api.StatusOverdue})
	h.saveAs("Second") // the one that is open
	h.press("h")
	for h.model.todos.sideItems()[h.model.todos.side].label != "First" {
		h.press("k")
	}
	h.press("S")
	if h.model.layer != layerForm {
		t.Fatal("S in the sidebar should open the menu")
	}
	h.mustContain("view  First")
	h.press("esc")

	// From the list it starts on the one that is open.
	h.press("esc", "S")
	h.mustContain("view  Second")
}
