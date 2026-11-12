package local

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// syncedAt is a backend signed in to the stand-in service and synced once,
// kept at path so a test can open it again as a restart would.
func syncedAt(t *testing.T, path, url string) *Backend {
	t.Helper()
	b, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SignIn(context.Background(), "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b
}

// listNames is the names of the lists the backend shows, sorted.
func listNames(t *testing.T, c *api.Client) []string {
	t.Helper()
	ls, err := c.ListLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range ls {
		out = append(out, l.Name)
	}
	slices.Sort(out)
	return out
}

// named is the account's list with a name, or nil.
func (s *service) named(name string) *api.List {
	for i := range s.lists {
		if s.lists[i].Name == name {
			return &s.lists[i]
		}
	}
	return nil
}

// Lists can be made, used, renamed and deleted with no connection, and it
// all survives a restart before the next sync, which sends it in order:
// new lists before the todos in them, deletions after the todos moved out.
func TestOfflineListChangesSurviveARestartAndGoUpInOrder(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists,
		api.List{ID: "work", Name: "Work", Role: "owner"},
		api.List{ID: "old", Name: "Old", Role: "owner"})
	path := filepath.Join(t.TempDir(), "todos.json")
	b := syncedAt(t, path, url)
	c := b.Client()
	keep, err := c.CreateTodos(ctx, api.NewTodo{Title: "Keep me", ListID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := c.CreateTodos(ctx, api.NewTodo{Title: "Goes with the list", ListID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	svc.down.Store(true)
	trip, err := c.CreateList(ctx, "Trip")
	if err != nil {
		t.Fatalf("creating a list offline: %v", err)
	}
	packed, err := c.CreateTodos(ctx, api.NewTodo{Title: "Pack", ListID: trip.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenameList(ctx, trip.ID, "Holiday"); err != nil {
		t.Fatalf("renaming a new list offline: %v", err)
	}
	if _, err := c.RenameList(ctx, "work", "Office"); err != nil {
		t.Fatalf("renaming a list offline: %v", err)
	}
	if _, _, err := c.UpdateTodo(ctx, keep[0].ID, api.TodoPatch{ListID: api.Val("p1")}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "old"); err != nil {
		t.Fatalf("deleting a list offline: %v", err)
	}
	if got := strings.Join(listNames(t, c), ","); got != "Holiday,Office,Personal" {
		t.Errorf("lists offline = %s", got)
	}
	if got := strings.Join(titles(t, c), ","); got != "Keep me,Pack" {
		t.Errorf("todos offline = %s, want the deleted list's todos gone", got)
	}
	if _, err := b.Sync(ctx); !Offline(err) {
		t.Fatalf("sync offline = %v", err)
	}
	// Create, rename and delete of lists, and the two todos.
	if st := b.Status(); st.Pending != 5 {
		t.Errorf("pending offline = %d, want 5", st.Pending)
	}

	// A restart before the connection is back.
	b2, err := Open(path, "evt_x", url)
	if err != nil {
		t.Fatal(err)
	}
	c2 := b2.Client()
	if got := strings.Join(listNames(t, c2), ","); got != "Holiday,Office,Personal" {
		t.Errorf("lists after a restart = %s", got)
	}
	svc.down.Store(false)
	svc.calls = nil
	if _, err := b2.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	want := []string{"POST /lists Holiday", "PATCH /lists/work", "POST /sync", "DELETE /lists/old"}
	if got := svc.calls; len(got) < len(want) || !slices.Equal(got[:3], want[:3]) || !slices.Contains(got, want[3]) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	holiday := svc.named("Holiday")
	switch {
	case holiday == nil:
		t.Fatal("the list made offline never reached the service")
	case svc.todos[packed[0].ID] == nil || svc.todos[packed[0].ID].ListID != holiday.ID:
		t.Errorf("the todo made in it is %+v, want it in %s", svc.todos[packed[0].ID], holiday.ID)
	}
	if svc.named("Office") == nil || svc.named("Work") != nil {
		t.Errorf("service lists = %+v, want Work renamed", svc.lists)
	}
	if svc.listAt("old") >= 0 {
		t.Error("the deleted list is still on the service")
	}
	if got := svc.todos[keep[0].ID]; got == nil || got.DeletedAt != nil || got.ListID != "p1" {
		t.Errorf("the todo moved out first is %+v, want it alive in the personal list", got)
	}
	if got := svc.todos[doomed[0].ID]; got == nil || got.DeletedAt == nil {
		t.Errorf("the todo left in the deleted list is %+v, want it deleted", got)
	}
	if got := strings.Join(listNames(t, c2), ","); got != "Holiday,Office,Personal" {
		t.Errorf("lists after sync = %s", got)
	}
	if st := b2.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status after sync = %+v", st)
	}
	// The todo kept here follows its list to the id the service gave it.
	got, err := c2.GetTodo(ctx, packed[0].ID)
	if err != nil || got.ListID != holiday.ID {
		t.Errorf("todo here = %+v %v, want list %s", got, err, holiday.ID)
	}
}

// A list made and deleted before any sync never reaches the service.
func TestAListMadeAndDeletedOfflineSendsNothing(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	svc.down.Store(true)
	l, err := c.CreateList(ctx, "Brief")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTodos(ctx, api.NewTodo{Title: "Never sent", ListID: l.ID}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); st.Pending != 0 {
		t.Errorf("pending = %d, want nothing to send", st.Pending)
	}
	svc.down.Store(false)
	svc.calls = nil
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(svc.calls, func(c string) bool { return strings.Contains(c, "/lists") }) || len(svc.todos) != 0 {
		t.Errorf("calls = %v, todos = %d, want nothing about the list", svc.calls, len(svc.todos))
	}
}

// A todo moved into a list that is then deleted is deleted on the service
// too, where it is still in its old list, rather than kept there and gone
// here.
func TestDeletingAListDeletesATodoMovedIntoIt(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "old", Name: "Old", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	todo := add(t, c, "Moved in")
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	svc.down.Store(true)
	if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{ListID: api.Val("old")}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	svc.down.Store(false)
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := svc.todos[todo.ID]; got.DeletedAt == nil {
		t.Errorf("service has %+v, want it deleted with the list it was moved to", got.Todo)
	}
	if st := b.Status(); st.Pending != 0 || st.Refused != 0 {
		t.Errorf("status = %+v", st)
	}
}

// What the service would refuse is refused here, at once: only the owner
// renames or deletes a list, and the personal list stays.
func TestListRulesAreCheckedOffline(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "shared", Name: "Shared", Role: "member"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	svc.down.Store(true)
	for name, err := range map[string]error{
		"rename shared":   func() error { _, err := c.RenameList(ctx, "shared", "Mine now"); return err }(),
		"delete shared":   c.DeleteList(ctx, "shared"),
		"delete personal": c.DeleteList(ctx, "p1"),
		"rename to blank": func() error { _, err := c.RenameList(ctx, "p1", "  "); return err }(),
		"rename too long": func() error { _, err := c.RenameList(ctx, "p1", strings.Repeat("x", 121)); return err }(),
	} {
		var apiErr *api.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode/100 != 4 {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	if st := b.Status(); st.Pending != 0 {
		t.Errorf("pending = %d after only refusals", st.Pending)
	}
	// The personal list can be renamed by its owner.
	if _, err := c.RenameList(ctx, "p1", "Mine"); err != nil {
		t.Errorf("renaming the personal list: %v", err)
	}
}

// A change the service turns down at sync time is reported by list name and
// undone here: a rename goes back to the service's name, and a deleted list
// comes back with its todos.
func TestRefusedListChangesAreReportedAndUndone(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists,
		api.List{ID: "a", Name: "Alpha", Role: "owner"},
		api.List{ID: "b", Name: "Beta", Role: "owner"})
	path := filepath.Join(t.TempDir(), "todos.json")
	b := syncedAt(t, path, url)
	c := b.Client()
	inBeta, err := c.CreateTodos(ctx, api.NewTodo{Title: "In Beta", ListID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	svc.down.Store(true)
	if _, err := c.RenameList(ctx, "a", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	// Meanwhile the lists stopped being this account's to change, as when
	// the service knows better than the last sync did.
	svc.lists[1].Role, svc.lists[2].Role = "member", "member"
	svc.down.Store(false)

	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatalf("sync = %v; a settled refusal is not a failed sync", err)
	}
	joined := strings.Join(res.ListsRefused, "; ")
	if len(res.ListsRefused) != 2 || !strings.Contains(joined, `"Renamed"`) || !strings.Contains(joined, `"Beta"`) || !strings.Contains(joined, "only the owner") {
		t.Errorf("refused = %q, want both changes named with the service's reason", res.ListsRefused)
	}
	if got := strings.Join(listNames(t, c), ","); got != "Alpha,Beta,Personal" {
		t.Errorf("lists after the refusals = %s, want the service's", got)
	}
	if got, err := c.GetTodo(ctx, inBeta[0].ID); err != nil || got.ListID != "b" {
		t.Errorf("the deleted list's todo = %+v %v, want it back", got, err)
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status = %+v, want nothing left to send", st)
	}
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.named("Alpha") == nil || svc.listAt("b") < 0 {
		t.Error("the refused changes reached the service after all")
	}
}

// A todo deleted on its own before its list was deleted stays deleted when
// the list's deletion is undone. The others in the list come back with it,
// as the person asked for the list to go, not for each of them.
func TestATodoDeletedBeforeItsListStaysDeletedWhenTheListComesBack(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "b", Name: "Beta", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	var made []api.Todo
	for _, title := range []string{"Dropped", "Kept"} {
		one, err := c.CreateTodos(ctx, api.NewTodo{Title: title, ListID: "b"})
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, one...)
	}
	mustSync(t, b)

	svc.down.Store(true)
	if err := c.DeleteTodo(ctx, made[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	svc.lists[1].Role = "member"
	svc.down.Store(false)

	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatalf("sync = %v; a settled refusal is not a failed sync", err)
	}
	if len(res.ListsRefused) != 1 {
		t.Errorf("refused = %q, want the deletion of Beta undone", res.ListsRefused)
	}
	if up := svc.todos[made[0].ID]; up == nil || up.DeletedAt == nil {
		t.Errorf("the service has %+v, want the todo deleted on its own deleted there", up)
	}
	if _, err := c.GetTodo(ctx, made[0].ID); api.CodeOf(err) != api.CodeNotFound {
		t.Errorf("the todo deleted on its own = %v, want it to stay deleted", err)
	}
	if got, err := c.GetTodo(ctx, made[1].ID); err != nil || got.ListID != "b" {
		t.Errorf("the list's other todo = %+v %v, want it back", got, err)
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status = %+v, want nothing left to send", st)
	}
}

// At the service's limit of lists, deleting one offline makes room for one
// made offline: the deletion goes first and the list is made in the same
// sync, though new lists normally go up before deletions.
func TestADeletionMakesRoomForANewListInOneSync(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	for i := len(svc.lists); i < 25; i++ {
		svc.lists = append(svc.lists, api.List{ID: fmt.Sprintf("l%d", i), Name: fmt.Sprintf("List %d", i), Role: "owner"})
	}
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	svc.down.Store(true)
	if err := c.DeleteList(ctx, "l1"); err != nil {
		t.Fatal(err)
	}
	fresh, err := c.CreateList(ctx, "Fresh")
	if err != nil {
		t.Fatal(err)
	}
	made, err := c.CreateTodos(ctx, api.NewTodo{Title: "In the fresh list", ListID: fresh.ID})
	if err != nil {
		t.Fatal(err)
	}
	svc.down.Store(false)
	if _, err := b.Sync(ctx); err != nil {
		t.Fatalf("sync = %v", err)
	}
	if l := svc.named("Fresh"); l == nil || svc.todos[made[0].ID] == nil || svc.todos[made[0].ID].ListID != l.ID {
		t.Errorf("lists %v, todo %+v: want the new list made with its todo", svc.lists, svc.todos[made[0].ID])
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status = %+v", st)
	}
}

// madeButNotSaved is a list made offline, with a todo in it, that the next
// sync makes in the account and then fails to save: the file cannot be
// read when the answer comes, as if another copy of evado had broken it.
// The file is put back as it was before the answer, which is what a crash
// then would leave, and opened again.
func madeButNotSaved(t *testing.T) (*service, *Backend, api.Todo) {
	t.Helper()
	ctx := context.Background()
	svc, url := newService(t)
	path := filepath.Join(t.TempDir(), "todos.json")
	first := syncedAt(t, path, url)
	c := first.Client()
	svc.down.Store(true)
	trip, err := c.CreateList(ctx, "Trip")
	if err != nil {
		t.Fatal(err)
	}
	packed, err := c.CreateTodos(ctx, api.NewTodo{Title: "Pack", ListID: trip.ID})
	if err != nil {
		t.Fatal(err)
	}
	svc.down.Store(false)
	var before []byte
	svc.listMade = func() {
		svc.listMade = nil
		before, err = os.ReadFile(path)
		if err == nil {
			err = os.WriteFile(path, []byte("{"), 0o600)
		}
	}
	if _, serr := first.Sync(ctx); serr == nil {
		t.Fatal("the sync saved over a file it could not read")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Open(path, "evt_x", url)
	if err != nil {
		t.Fatal(err)
	}
	return svc, b, packed[0]
}

// The list is created with an id kept here before it is sent, so the next
// sync asks for the same list again and finds it, rather than making it a
// second time.
func TestAListMadeButNotSavedIsNotMadeAgain(t *testing.T) {
	svc, b, packed := madeButNotSaved(t)
	if _, err := b.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	trips := slices.DeleteFunc(slices.Clone(svc.lists), func(l api.List) bool { return l.Name != "Trip" })
	if len(trips) != 1 {
		t.Fatalf("the service has %d lists named Trip, want 1: %+v", len(trips), svc.lists)
	}
	if len(svc.listIDs) != 2 || svc.listIDs[0] == "" || svc.listIDs[1] != svc.listIDs[0] || trips[0].ID != svc.listIDs[0] {
		t.Errorf("ids sent = %q, list %s, want the same id both times", svc.listIDs, trips[0].ID)
	}
	if got := svc.todos[packed.ID]; got == nil || got.ListID != trips[0].ID {
		t.Errorf("the todo made in it is %+v, want it in %s", got, trips[0].ID)
	}
	if got := strings.Join(listNames(t, b.Client()), ","); got != "Personal,Trip" {
		t.Errorf("lists here = %s", got)
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status = %+v", st)
	}
}

// If the list made by the attempt that was not saved is deleted elsewhere
// before the next sync, the service will not make it again under that id,
// and it is not made again under another: it leaves here as any list
// deleted elsewhere does, and the todo made in it, which never went up,
// moves to the personal list and goes up there.
func TestAListDeletedElsewhereBeforeItsCreateWasSavedLeaves(t *testing.T) {
	svc, b, packed := madeButNotSaved(t)
	svc.dropList(svc.listIDs[0])
	res, err := b.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.listIDs) != 2 || svc.listIDs[1] != svc.listIDs[0] || svc.named("Trip") != nil {
		t.Errorf("ids sent = %q, lists %+v, want the list not made again", svc.listIDs, svc.lists)
	}
	want := GoneList{Name: "Trip", Into: "Personal", Moved: []string{"Pack"}}
	if len(res.ListsGone) != 1 || res.ListsGone[0].Name != want.Name || res.ListsGone[0].Into != want.Into ||
		!slices.Equal(res.ListsGone[0].Moved, want.Moved) || len(res.ListsGone[0].Lost)+len(res.ListsGone[0].Edits) != 0 {
		t.Errorf("gone = %+v, want %+v", res.ListsGone, want)
	}
	if got := svc.todos[packed.ID]; got == nil || got.DeletedAt != nil || got.ListID != "p1" {
		t.Errorf("the service has %+v, want the todo in the personal list", got)
	}
	if got := strings.Join(listNames(t, b.Client()), ","); got != "Personal" {
		t.Errorf("lists here = %s, want the deleted one gone", got)
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil || !res.Changed {
		t.Errorf("status = %+v, changed %v", st, res.Changed)
	}
}

// An id the account may not use is refused, and the list is made without
// one, as the service chooses.
func TestAListWhoseIDIsTakenIsMadeWithoutIt(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	svc.down.Store(true)
	trip, err := c.CreateList(ctx, "Trip")
	if err != nil {
		t.Fatal(err)
	}
	packed, err := c.CreateTodos(ctx, api.NewTodo{Title: "Pack", ListID: trip.ID})
	if err != nil {
		t.Fatal(err)
	}
	taken := NewID()
	b.mu.Lock()
	b.list(trip.ID).CreateID = taken
	err = b.persist()
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	svc.foreignLists[taken] = true
	svc.down.Store(false)
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	made := svc.named("Trip")
	if !slices.Equal(svc.listIDs, []string{taken, ""}) || made == nil || made.ID == taken {
		t.Fatalf("ids sent = %q, list %+v, want the taken id and then none", svc.listIDs, made)
	}
	if got := svc.todos[packed[0].ID]; got == nil || got.ListID != made.ID {
		t.Errorf("the todo made in it is %+v, want it in %s", got, made.ID)
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status = %+v", st)
	}
}

// A rename made while the list's creation is on its way is not lost when
// the service answers with the name it was created with.
func TestRenameDuringCreationIsKept(t *testing.T) {
	b := open(t, "", "")
	b.mu.Lock()
	b.st.UserID = "u1"
	b.st.Lists = append(b.st.Lists, List{Local: true, List: api.List{ID: "tmp", Name: "Renamed meanwhile", Role: "owner"}})
	b.bindList("tmp", api.List{ID: "real", Name: "As sent", Role: "owner"})
	l := b.list("real")
	b.mu.Unlock()
	if l == nil || l.Name != "Renamed meanwhile" || !l.Renamed || l.Was != "As sent" {
		t.Errorf("list = %+v, want the new name queued as a rename", l)
	}

	// And one deleted meanwhile is deleted there too.
	b.mu.Lock()
	b.bindList("gone", api.List{ID: "made", Name: "Gone", Role: "owner"})
	queued := len(b.st.Deleted) == 1 && b.st.Deleted[0].ID == "made"
	b.mu.Unlock()
	if !queued {
		t.Error("a list deleted while it was being made is left on the service")
	}

	// But one bound already, by another copy of evado that sent the same
	// id, is the same list, and stays.
	b.mu.Lock()
	b.bindList("tmp", api.List{ID: "real", Name: "As sent", Role: "owner"})
	kept := b.list("real") != nil && len(b.st.Deleted) == 1
	b.mu.Unlock()
	if !kept {
		t.Error("a list made once and bound twice is queued for deletion")
	}
}

// A todo moved out of a list that is then deleted must not go with it when
// the service turns the move down: the deletion is undone and reported, as
// one the service refused itself, and the todo stays. A list with no such
// todo is deleted as ever.
func TestARefusedMoveOutOfADeletedListKeepsTheTodo(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists,
		api.List{ID: "trip", Name: "Trip", Role: "owner"},
		api.List{ID: "plain", Name: "Plain", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	rent, err := c.CreateTodos(ctx, api.NewTodo{Title: "Pay rent", ListID: "trip"})
	if err != nil {
		t.Fatal(err)
	}
	mustSync(t, b)

	svc.down.Store(true)
	if _, _, err := c.UpdateTodo(ctx, rent[0].ID, api.TodoPatch{ListID: api.Val("p1")}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "trip"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "plain"); err != nil {
		t.Fatal(err)
	}
	svc.refuse = rent[0].ID
	svc.down.Store(false)

	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatalf("sync = %v; a settled refusal is not a failed sync", err)
	}
	if len(res.ListsRefused) != 1 || res.ListsRefused[0] != `deleting "Trip": 'Pay rent' could not be moved out of it` {
		t.Errorf("refused = %q, want the deletion of Trip undone and why", res.ListsRefused)
	}
	if up := svc.todos[rent[0].ID]; up == nil || up.DeletedAt != nil || up.ListID != "trip" {
		t.Errorf("the service has %+v, want the todo kept in Trip", up)
	}
	if svc.named("Trip") == nil || svc.named("Plain") != nil {
		t.Errorf("lists on the service = %v, want Trip kept and Plain deleted", svc.lists)
	}
	if got := get(t, c, rent[0].ID); got.ListID != "trip" {
		t.Errorf("the todo here = %+v, want it back in Trip", got)
	}
	if got := strings.Join(listNames(t, c), ","); got != "Personal,Trip" {
		t.Errorf("lists here = %s, want Trip back", got)
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status = %+v", st)
	}
}

// A move that has not been settled, because the list it goes to could not
// be made yet, holds the deletion back until it can go.
func TestADeletedListWaitsForAMoveThatCannotGoYet(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "trip", Name: "Trip", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	rent, err := c.CreateTodos(ctx, api.NewTodo{Title: "Pay rent", ListID: "trip"})
	if err != nil {
		t.Fatal(err)
	}
	mustSync(t, b)

	svc.down.Store(true)
	home, err := c.CreateList(ctx, "Home")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.UpdateTodo(ctx, rent[0].ID, api.TodoPatch{ListID: api.Val(home.ID)}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "trip"); err != nil {
		t.Fatal(err)
	}
	svc.down.Store(false)
	svc.listStatus = http.StatusBadRequest

	res, err := b.Sync(ctx)
	if err == nil || !strings.Contains(err.Error(), `could not create the list "Home"`) {
		t.Fatalf("sync = %v, want the list that could not be made", err)
	}
	if up := svc.todos[rent[0].ID]; up == nil || up.DeletedAt != nil || svc.named("Trip") == nil {
		t.Errorf("the service has %+v and lists %v, want the todo and Trip kept for now", up, svc.lists)
	}
	if len(res.ListsRefused) != 0 {
		t.Errorf("refused = %q, want nothing: the deletion only waits", res.ListsRefused)
	}
	if len(res.ListsWaiting) != 1 || !strings.Contains(res.ListsWaiting[0], "Trip") || !strings.Contains(res.ListsWaiting[0], "Home") {
		t.Fatalf("held deletion lacks a reason: %v", res.ListsWaiting)
	}

	svc.listStatus = 0
	mustSync(t, b)
	home2 := svc.named("Home")
	if home2 == nil || svc.named("Trip") != nil {
		t.Fatalf("lists on the service = %v, want Home made and Trip deleted", svc.lists)
	}
	if up := svc.todos[rent[0].ID]; up.DeletedAt != nil || up.ListID != home2.ID {
		t.Errorf("the service has %+v, want the todo moved to Home before Trip went", up)
	}
}
