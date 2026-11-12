package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
)

// account is the part of Evado that signing in and syncing talk to.
type account struct {
	todos   map[string]map[string]json.RawMessage
	existed []api.Todo
	// made are the lists created in the account, besides the personal one.
	made  []api.List
	clock time.Time
	// down makes every request fail as if the network were gone.
	down atomic.Bool
	// requests counts every request that arrived, answered or not.
	requests atomic.Int32
	// hold, when set, keeps each request waiting until it is closed.
	hold atomic.Pointer[chan struct{}]
}

func newAccount(t *testing.T) (*account, string) {
	a := &account{todos: map[string]map[string]json.RawMessage{}, clock: time.Now().Add(time.Hour)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests.Add(1)
		if hold := a.hold.Load(); hold != nil {
			<-*hold
		}
		if a.down.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		lists := append([]api.List{{ID: "p1", Name: "Personal", IsPersonal: true, Role: "owner"}}, a.made...)
		switch r.URL.Path {
		case "/lists":
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			l := api.List{ID: "remote-" + body.Name, Name: body.Name, Role: "owner"}
			a.made = append(a.made, l)
			w.WriteHeader(http.StatusCreated)
			write(map[string]any{"list": l})
		case "/me":
			if r.Header.Get("Authorization") != "Bearer evt_good" {
				w.WriteHeader(http.StatusUnauthorized)
				write(map[string]any{"error": "bad token", "code": "unauthorized"})
				return
			}
			write(map[string]any{"user": api.User{ID: "u1", Email: "me@example.com", TZ: "UTC"}, "lists": lists})
		case "/sync":
			var body struct {
				Changes []map[string]json.RawMessage `json:"changes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.clock = a.clock.Add(time.Second)
			for _, ch := range body.Changes {
				var id string
				_ = json.Unmarshal(ch["id"], &id)
				if a.todos[id] == nil {
					a.todos[id] = map[string]json.RawMessage{}
				}
				for k, v := range ch {
					a.todos[id][k] = v
				}
			}
			// Like the service, the pull gives back everything it holds,
			// what was just pushed included.
			out := append([]api.Todo{}, a.existed...)
			for id, ch := range a.todos {
				todo := api.Todo{ID: id, ListID: "p1", UpdatedAt: a.clock}
				_ = json.Unmarshal(ch["title"], &todo.Title)
				_ = json.Unmarshal(ch["isCompleted"], &todo.IsCompleted)
				out = append(out, todo)
			}
			write(map[string]any{"todos": out, "lists": lists, "cursor": "1", "hasMore": false})
		default:
			w.WriteHeader(http.StatusNotFound)
			write(map[string]any{"error": "not found", "code": "not_found"})
		}
	}))
	t.Cleanup(srv.Close)
	return a, srv.URL
}

// offlineHarness starts the TUI keeping its todos in a private directory,
// the way it runs for a person, rather than straight against a server.
func offlineHarness(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	t.Setenv("EVADO_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return restart(t, cfg)
}

// restart is a new run of the program with the same files.
func restart(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	m := New(context.Background(), cfg)
	if m.backend == nil {
		t.Fatalf("the todos on this computer did not open: %v", m.startupErr)
	}
	h := &harness{t: t, model: m}
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.run(m.Init())
	return h
}

func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		// A space typed into a field carries its rune, unlike the space
		// the harness sends as a command key.
		if r == ' ' {
			h.send(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			h.run(h.cmds...)
			h.cmds = nil
			continue
		}
		h.press(string(r))
	}
}

func TestFirstRunOffersGoingWithoutAnAccount(t *testing.T) {
	h := offlineHarness(t, config.Config{})
	if h.model.layer != layerLogin {
		t.Fatalf("layer = %v, want the sign-in screen", h.model.layer)
	}
	h.mustContain("Use without an account")
	h.press("tab", "tab", "enter")
	if h.model.layer != layerNone {
		t.Fatalf("layer = %v, want the app", h.model.layer)
	}
	h.mustContain("on this computer")
	h.mustContain("Personal")

	disk, err := config.Load()
	if err != nil || !disk.Local || disk.Token != "" {
		t.Errorf("saved config = %+v %v, want the choice remembered and no token", disk, err)
	}
}

func TestTodosSurviveARestartWithNoAccount(t *testing.T) {
	cfg := config.Config{Local: true}
	h := offlineHarness(t, cfg)
	h.press("n")
	h.typeText("Call Sam tomorrow")
	h.press("enter")
	h.mustContain("Call Sam")
	if sel := h.model.todos.selected(); sel == nil || sel.Title != "Call Sam" || sel.ScheduledDay == nil {
		t.Fatalf("created %+v, want the date read out of the title with no server", sel)
	}
	h.press("space")

	again := restart(t, cfg)
	again.mustContain("Call Sam")
	if got := again.model.todos.stats().done; got != 1 {
		t.Errorf("done after restart = %d, want the completion kept", got)
	}
	// Sharing is the account's business, and says so rather than failing
	// as if the token were bad.
	again.press("2", "i", "enter")
	if again.model.layer == layerLogin {
		t.Error("an account-only action threw the user out to the sign-in screen")
	}
	if !strings.Contains(again.model.toastText(), "account") {
		t.Errorf("toast = %q, want it to say an account is needed", again.model.toastText())
	}
}

func TestSigningInSendsTheTodosUp(t *testing.T) {
	acct, url := newAccount(t)
	h := offlineHarness(t, config.Config{Local: true, BaseURL: url})
	h.press("n")
	h.typeText("Made before signing in")
	h.press("enter")

	h.press("4")
	h.model.acct.selectRow("computer")
	h.mustContain("on this computer only")
	h.press("c")
	if h.model.layer != layerLogin {
		t.Fatalf("layer = %v, want the sign-in screen", h.model.layer)
	}
	h.typeText("evt_good")
	h.press("enter")

	if len(acct.todos) != 1 {
		t.Fatalf("the account holds %d todos, want the one made here", len(acct.todos))
	}
	if st := h.model.backend.Status(); !st.SignedIn || st.Pending != 0 {
		t.Errorf("status = %+v, want signed in with nothing left to send", st)
	}
	h.mustContain("synced")
	disk, err := config.Load()
	if err != nil || disk.Token != "evt_good" || disk.Local {
		t.Errorf("saved config = %+v %v, want the accepted token", disk, err)
	}

	// From here a change goes up by itself on the next sync.
	h.press("1", "g", "space")
	h.run(h.model.syncNow())
	completed := false
	for _, ch := range acct.todos {
		completed = completed || string(ch["isCompleted"]) == "true"
	}
	if !completed {
		t.Error("the completion never reached the account")
	}
}

func TestARejectedTokenChangesNothing(t *testing.T) {
	_, url := newAccount(t)
	h := offlineHarness(t, config.Config{Local: true, BaseURL: url})
	h.press("4")
	h.model.acct.selectRow("computer")
	h.press("c")
	h.typeText("evt_wrong")
	h.press("enter")
	if h.model.layer != layerLogin {
		t.Fatalf("layer = %v, want to be asked again", h.model.layer)
	}
	h.mustContain("rejected")
	disk, _ := config.Load()
	if disk.Token != "" || !disk.Local {
		t.Errorf("saved config = %+v, want it untouched by a token that was refused", disk)
	}
	// Esc goes back to the todos instead of quitting.
	h.press("esc")
	if h.model.layer != layerNone || h.model.quited {
		t.Errorf("layer = %v quit = %v, want the app back", h.model.layer, h.model.quited)
	}
}

func TestMergeIsAskedAndCanBeDeclined(t *testing.T) {
	acct, url := newAccount(t)
	acct.existed = []api.Todo{{ID: "aaaaaaaa-0000-4000-8000-000000000001", ListID: "p1", Title: "Already in the account", UpdatedAt: time.Now()}}
	h := offlineHarness(t, config.Config{Local: true, BaseURL: url})
	h.press("n")
	h.typeText("Only on this computer")
	h.press("enter")

	h.press("4")
	h.model.acct.selectRow("computer")
	h.press("c")
	h.typeText("evt_good")
	h.press("enter")
	if h.model.layer != layerConfirm {
		t.Fatalf("layer = %v, want to be asked before merging", h.model.layer)
	}
	h.mustContain("This computer has 1 todo and the account has 1 todo")

	h.press("esc")
	if len(acct.todos) != 0 {
		t.Error("declining still sent todos to the account")
	}
	if h.model.backend.Status().SignedIn {
		t.Error("declining still signed in")
	}
	if disk, _ := config.Load(); disk.Token != "" {
		t.Error("declining still saved the token")
	}

	// Asked again, saying yes merges both ways.
	h.press("c")
	h.typeText("evt_good")
	h.press("enter", "enter")
	if len(acct.todos) != 1 {
		t.Errorf("the account received %d todos, want the one from this computer", len(acct.todos))
	}
	h.press("1")
	h.mustContain("Already in the account")
	h.mustContain("Only on this computer")
}

func TestSignOutWarnsAboutTodosThatExistNowhereElse(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.press("n")
	h.typeText("Precious")
	h.press("enter")
	h.press("4")
	h.model.acct.selectRow("signout")
	h.press("enter")
	h.mustContain("exist nowhere else")
	// A destructive prompt needs a second press.
	h.press("enter")
	if h.model.layer != layerConfirm {
		t.Fatal("one enter should not be enough to delete the only copy")
	}
	h.press("esc")
	h.press("1")
	h.mustContain("Precious")
}

// Once signed in, lists can still be made, renamed and deleted with no
// connection, and deleting one says that it reaches the account later.
func TestListsChangeOfflineOnceSignedIn(t *testing.T) {
	acct, url := newAccount(t)
	h := offlineHarness(t, config.Config{Token: "evt_good", BaseURL: url})
	if !h.model.backend.Status().SignedIn {
		t.Fatal("not signed in")
	}
	acct.down.Store(true)
	h.run(h.model.syncNow())

	h.press("2", "n")
	h.typeText("Trip")
	h.press("enter")
	h.mustContain("Trip")
	h.press("G", "e")
	h.typeText("s")
	h.press("enter")
	h.mustContain("Trips")
	if st := h.model.backend.Status(); st.Pending != 1 {
		t.Errorf("pending = %d, want the new list waiting to be sent", st.Pending)
	}

	h.press("d")
	h.mustContain("You are offline")
	h.press("y", "enter")
	h.mustNotContain("Trips")

	h.press("g", "d")
	h.mustContain("personal list cannot be deleted")
}

// A list made while offline is not in the account until a sync makes it
// there. The Lists tab says so, and sharing it or reading its members says
// the same instead of asking the service about a list it has never heard of.
func TestAListNotSyncedYetCannotBeSharedOrAskedForItsMembers(t *testing.T) {
	acct, url := newAccount(t)
	h := offlineHarness(t, config.Config{Token: "evt_good", BaseURL: url})
	acct.down.Store(true)
	h.run(h.model.syncNow())
	h.press("2", "n")
	h.typeText("Trip")
	h.press("enter")
	h.press("G")
	h.mustContain("Not synced yet")
	h.mustContain("unsynced")
	acct.down.Store(false)

	for _, key := range []string{"a", "m", "i"} {
		h.press(key)
		if h.model.layer != layerNone || h.model.busy {
			t.Errorf("%s: layer %v, busy %v; want nothing opened or asked of the service", key, h.model.layer, h.model.busy)
		}
		if got := h.model.toastText(); !strings.Contains(got, "this list has not been synced yet; try again after a sync") {
			t.Errorf("%s: toast = %q", key, got)
		}
		h.model.toast = nil
	}

	// The sync makes it, and the notes go.
	h.run(h.model.syncNow())
	h.mustNotContain("Not synced yet")
	h.mustNotContain("unsynced")
}

// With no account every list is only here, so none is marked.
func TestNoListIsMarkedNotSyncedWithoutAnAccount(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.press("2", "n")
	h.typeText("Trip")
	h.press("enter")
	h.mustContain("Trip")
	h.mustNotContain("Not synced yet")
	h.mustNotContain("unsynced")
}

func TestTheServiceNotSyncedAnswerIsWordedForTheForm(t *testing.T) {
	got := inviteProblem(&api.Error{StatusCode: http.StatusConflict, Code: api.CodeListNotSynced, Message: local.NotSyncedMessage})
	if got != "This list has not been synced yet. Try again after a sync." {
		t.Errorf("inviteProblem = %q", got)
	}
}

// An edit that a newer change on the service replaced is gone from the
// screen after the sync, so a warning says which, once.
func TestASyncSaysWhichEditsWereReplaced(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.send(syncedMsg{res: local.Result{Replaced: []local.Replacement{
		{ID: "a", Title: "Pay rent", Fields: []string{"title"}},
	}}})
	h.run(h.cmds...)
	h.cmds = nil
	if got := h.model.toastText(); !strings.Contains(got, "1 edit was replaced by a newer change: 'Pay rent' (title)") {
		t.Errorf("toast = %q", got)
	}

	h.send(syncedMsg{res: local.Result{Replaced: []local.Replacement{
		{ID: "a", Title: "Pay rent", Fields: []string{"title", "due"}},
		{ID: "b", Title: "Buy milk", Deleted: true},
	}}})
	h.run(h.cmds...)
	h.cmds = nil
	if got := h.model.toastText(); !strings.Contains(got, "2 edits were replaced by newer changes: 'Pay rent' (title, due), 'Buy milk' (deleted)") {
		t.Errorf("toast = %q", got)
	}
}

// A list that left the account takes its unsent todos along, or has them
// moved; either way a warning says what became of them, once.
func TestASyncSaysWhatBecameOfTheTodosInAListThatLeft(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.send(syncedMsg{res: local.Result{ListsGone: []local.GoneList{
		{Name: "Trip", Into: "Inbox", Moved: []string{"Book the ferry", "Pack"}},
	}}})
	h.run(h.cmds...)
	h.cmds = nil
	if got := h.model.toastText(); !strings.Contains(got, "2 todos made here were moved to Inbox: the list 'Trip' is gone from the account") {
		t.Errorf("toast = %q", got)
	}

	h.send(syncedMsg{res: local.Result{ListsGone: []local.GoneList{
		{Name: "Trip", Edits: []string{"Pay rent"}},
	}}})
	h.run(h.cmds...)
	h.cmds = nil
	if got := h.model.toastText(); !strings.Contains(got, "1 edit was lost with the list 'Trip', which is gone from the account: 'Pay rent'") {
		t.Errorf("toast = %q", got)
	}

	h.send(syncedMsg{res: local.Result{ListsGone: []local.GoneList{
		{Name: "Trip", Lost: []string{"Book the ferry"}},
	}}})
	h.run(h.cmds...)
	h.cmds = nil
	if got := h.model.toastText(); !strings.Contains(got, "1 todo made here was lost with the list 'Trip', which is gone from the account: 'Book the ferry'") {
		t.Errorf("toast = %q", got)
	}
}

// A sync with several things to say shows each of them, one after another,
// rather than only the last: a warning may be the only notice that an edit
// was lost.
func TestASyncWithSeveralWarningsShowsEachInTurn(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.send(syncedMsg{res: local.Result{
		Refused:      []string{"Buy milk"},
		Replaced:     []local.Replacement{{ID: "a", Title: "Pay rent", Fields: []string{"title"}}},
		ListsGone:    []local.GoneList{{Name: "Trip", Into: "Inbox", Moved: []string{"Pack"}}},
		ListsRefused: []string{"deleting 'Club'"},
	}})
	h.run(h.cmds...)
	h.cmds = nil

	want := []string{
		"the server refused 1 change: Buy milk",
		"1 edit was replaced by a newer change: 'Pay rent' (title)",
		"1 todo made here was moved to Inbox: the list 'Trip' is gone",
		"the server refused 1 list change, now undone: deleting 'Club'",
	}
	for i, w := range want {
		if got := h.model.toastText(); !strings.Contains(got, w) {
			t.Fatalf("warning %d = %q, want %q", i+1, got, w)
		}
		// Each stays for as long as any toast does, then the next comes.
		for range 3 {
			h.send(tickMsg(time.Now()))
		}
	}
	if got := h.model.toastText(); got != "" {
		t.Errorf("after the last warning the toast is %q, want none", got)
	}
}

// The sign-in screen shows no toasts, so the warnings of a sync that ends
// while it is up wait for it to close instead of expiring unseen.
func TestSyncWarningsWaitOutTheSignInScreen(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.send(syncedMsg{res: local.Result{Refused: []string{"Buy milk"}}})
	h.send(todosMsg{err: &api.Error{StatusCode: http.StatusUnauthorized, Code: api.CodeUnauthorized}})
	h.send(syncedMsg{res: local.Result{
		Replaced: []local.Replacement{{ID: "a", Title: "Pay rent", Fields: []string{"title"}}},
	}})
	h.run(h.cmds...)
	h.cmds = nil
	if h.model.layer != layerLogin {
		t.Fatalf("layer = %v, want the sign-in screen", h.model.layer)
	}
	for range 10 {
		h.send(tickMsg(time.Now()))
	}

	h.typeText("evt_test")
	h.press("enter")
	if h.model.layer != layerNone {
		t.Fatalf("layer = %v after signing in again", h.model.layer)
	}
	var seen []string
	for range 12 {
		seen = append(seen, h.model.toastText())
		h.send(tickMsg(time.Now()))
	}
	said := strings.Join(seen, "\n")
	for _, w := range []string{
		"the server refused 1 change: Buy milk",
		"1 edit was replaced by a newer change: 'Pay rent' (title)",
		"token saved",
	} {
		if !strings.Contains(said, w) {
			t.Errorf("never shown: %q\n---\n%s", w, said)
		}
	}
}

// Warnings that answer what you just did still replace one another.
func TestAWarningAfterAKeyPressReplacesTheLastOne(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.send(toastMsg{kind: toastWarn, text: "first"})
	h.send(toastMsg{kind: toastWarn, text: "second"})
	if got := h.model.toastText(); got != "second" {
		t.Errorf("toast = %q, want the newer one", got)
	}
	for range 3 {
		h.send(tickMsg(time.Now()))
	}
	if got := h.model.toastText(); got != "" {
		t.Errorf("toast = %q, want the first not to come back", got)
	}
}

func TestThePollWaitsOutTheSyncBackoff(t *testing.T) {
	acct, url := newAccount(t)
	h := offlineHarness(t, config.Config{Token: "evt_good", BaseURL: url})
	acct.down.Store(true)
	h.run(h.model.syncNow())
	if !time.Now().Before(h.model.syncRetry) {
		t.Fatal("a failed sync set no backoff")
	}

	before := acct.requests.Load()
	h.run(h.model.onPoll())
	if n := acct.requests.Load() - before; n != 0 {
		t.Errorf("the poll sent %d requests during the backoff, want none", n)
	}

	// Once the backoff is over the poll syncs again.
	h.model.syncRetry = time.Now().Add(-time.Second)
	h.run(h.model.onPoll())
	if acct.requests.Load() == before {
		t.Error("the poll did not sync once the backoff was over")
	}
}

func TestRefreshingDuringASyncKeepsItsBackoff(t *testing.T) {
	acct, url := newAccount(t)
	h := offlineHarness(t, config.Config{Token: "evt_good", BaseURL: url})
	acct.down.Store(true)
	h.run(h.model.syncNow())
	if h.model.syncBackoff != minBackoff {
		t.Fatalf("backoff = %v after one failure, want %v", h.model.syncBackoff, minBackoff)
	}

	// The next sync is held on its way to the server, so it is still
	// running when r is pressed.
	hold := make(chan struct{})
	acct.hold.Store(&hold)
	sync := h.model.syncNow()
	answer := make(chan tea.Msg)
	go func() { answer <- sync() }()
	for deadline := time.Now().Add(5 * time.Second); !h.model.backend.Status().Syncing; {
		if time.Now().After(deadline) {
			t.Fatal("the sync never started")
		}
		time.Sleep(time.Millisecond)
	}
	h.press("r")
	acct.hold.Store(nil)
	close(hold)
	h.send(<-answer)
	h.run(h.cmds...)
	h.cmds = nil

	if h.model.syncBackoff != 2*minBackoff {
		t.Errorf("backoff = %v after a second failure, want %v", h.model.syncBackoff, 2*minBackoff)
	}
	if h.model.syncQueued {
		t.Error("a sync is still marked as on its way")
	}
}
