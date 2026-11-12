package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// A running TUI and commands run next to it each hold a backend on the same
// file. Every write from either side has to survive the other's.
func TestCommandsRacingARunningBackendLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	tui, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const each = 25
	var wg sync.WaitGroup
	errs := make(chan error, 4*each)

	// The TUI's backend lives for the whole run and writes from several
	// goroutines, as its commands do.
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := tui.Client()
			for i := 0; i < each; i++ {
				if _, err := c.CreateTodos(ctx, api.NewTodo{Title: fmt.Sprintf("tui %d-%d", g, i)}); err != nil {
					errs <- err
				}
			}
		}()
	}
	// Each command opens the file afresh, writes once and exits, as the
	// CLI does.
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				cli, err := Open(path, "", "")
				if err != nil {
					errs <- err
					continue
				}
				if _, err := cli.Client().CreateTodos(ctx, api.NewTodo{Title: fmt.Sprintf("cli %d-%d", g, i)}); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	want := []string{}
	for g := 0; g < 2; g++ {
		for i := 0; i < each; i++ {
			want = append(want, fmt.Sprintf("tui %d-%d", g, i), fmt.Sprintf("cli %d-%d", g, i))
		}
	}
	slices.Sort(want)
	fresh, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]*Backend{"the running backend": tui, "a new one": fresh} {
		got := titles(t, b.Client())
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s has %d todos, want %d", name, len(got), len(want))
		}
	}
}

// A backend that is already open sees what another one saved without being
// opened again, and its own next change keeps it.
func TestABackendSeesAnotherOnesChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	tui, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	made, err := tui.Client().CreateTodos(ctx, api.NewTodo{Title: "Pay rent"})
	if err != nil {
		t.Fatal(err)
	}
	id := made[0].ID
	if n := tui.Reloads(); n != 0 {
		t.Fatalf("Reloads = %d before anyone else wrote, want 0", n)
	}

	cli, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	notes := "from the command line"
	if _, _, err := cli.Client().UpdateTodo(ctx, id, api.TodoPatch{Notes: api.Val(notes)}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Client().CreateTodos(ctx, api.NewTodo{Title: "Water plants"}); err != nil {
		t.Fatal(err)
	}

	if n := tui.Reloads(); n != 1 {
		t.Errorf("Reloads = %d after another backend wrote, want 1", n)
	}
	// An edit made in the TUI afterwards changes its own field and leaves
	// the other one's alone.
	title := "Pay the rent"
	got, _, err := tui.Client().UpdateTodo(ctx, id, api.TodoPatch{Title: api.Val(title)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Notes == nil || *got.Notes != notes {
		t.Errorf("notes = %v after the TUI's edit, want the command's %q", got.Notes, notes)
	}
	again, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ts := titles(t, again.Client()); !slices.Contains(ts, "Water plants") || !slices.Contains(ts, "Pay the rent") {
		t.Errorf("on disk: %v, want both sides' changes", ts)
	}
	if n := tui.Reloads(); n != 1 {
		t.Errorf("Reloads = %d after the backend's own write, want still 1", n)
	}
}

// A file another copy of evado left unreadable, for instance a newer
// version's, is not saved over with what this one remembers.
func TestAnUnreadableFileIsNotSavedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	b, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := b.Client().CreateTodos(ctx, api.NewTodo{Title: "first"}); err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"version": 99, "lists": [{"id": "x"}], "todos": []}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	// Some filesystems keep times to the second, and the size alone could
	// match; a different file is enough to be noticed.
	if _, err := b.Client().CreateTodos(ctx, api.NewTodo{Title: "second"}); err == nil {
		t.Error("a write over a file this version cannot read went through")
	}
	if raw, _ := os.ReadFile(path); string(raw) != string(newer) {
		t.Errorf("the file was replaced: %s", raw)
	}
}

// A copy of evado that holds the file too long makes a write fail with a
// reason rather than wait forever or go ahead without it.
func TestAHeldFileTimesOut(t *testing.T) {
	wait := diskWait
	diskWait = 200 * time.Millisecond
	t.Cleanup(func() { diskWait = wait })
	path := filepath.Join(t.TempDir(), "todos.json")
	b, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	start := time.Now()
	_, err = b.Client().CreateTodos(context.Background(), api.NewTodo{Title: "blocked"})
	if err == nil {
		t.Fatal("a write went through while another held the file")
	}
	if waited := time.Since(start); waited < diskWait {
		t.Errorf("gave up after %v, want at least %v", waited, diskWait)
	}
}

// Signing out removes the file only while this copy holds it and has read
// it. Removing it otherwise could undo another copy's save, or a newer
// version's file this one cannot read. Nor does a later save, once the file
// is free again, finish the removal behind the person's back.
func TestForgetLeavesAFileItCannotHoldOrRead(t *testing.T) {
	wait := diskWait
	diskWait = 50 * time.Millisecond
	t.Cleanup(func() { diskWait = wait })
	newer := []byte(`{"version": 99, "lists": [{"id": "x"}], "todos": []}`)
	for _, tc := range []struct {
		name string
		// block stops the backend from using the file and returns what
		// frees it again, or nil when nothing does.
		block func(t *testing.T, path string) func()
	}{
		{"held by another copy", func(t *testing.T, path string) func() {
			f, err := lockFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return func() { _ = f.Close() }
		}},
		{"unreadable", func(t *testing.T, path string) func() {
			if err := os.WriteFile(path, newer, 0o600); err != nil {
				t.Fatal(err)
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "todos.json")
			b, err := Open(path, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Client().CreateTodos(context.Background(), api.NewTodo{Title: "Keep me"}); err != nil {
				t.Fatal(err)
			}
			free := tc.block(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Forget(); err == nil {
				t.Error("Forget reported success without holding and reading the file")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("the file was removed: %v", err)
			}
			if string(after) != string(before) {
				t.Errorf("the file changed: %s", after)
			}
			if free == nil {
				return
			}
			free()
			if _, err := b.Client().CreateTodos(context.Background(), api.NewTodo{Title: "Later"}); err != nil {
				t.Fatal(err)
			}
			if ts := titles(t, b.Client()); !slices.Contains(ts, "Keep me") {
				t.Errorf("after the next save: %v, want the todo kept", ts)
			}
		})
	}
}

// A command writing while the TUI has offline list changes queued keeps
// them, and the TUI's next change and sync keep the command's todo.
func TestQueuedListChangesSurviveACommandWritingInBetween(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists,
		api.List{ID: "work", Name: "Work", Role: "owner"},
		api.List{ID: "old", Name: "Old", Role: "owner"})
	path := filepath.Join(t.TempDir(), "todos.json")
	tui := syncedAt(t, path, url)
	c := tui.Client()

	svc.down.Store(true)
	trip, err := c.CreateList(ctx, "Trip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenameList(ctx, "work", "Office"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "old"); err != nil {
		t.Fatal(err)
	}

	cli, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	made, err := cli.Client().CreateTodos(ctx, api.NewTodo{Title: "From the command line", ListID: trip.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listNames(t, cli.Client()), ","); got != "Office,Personal,Trip" {
		t.Errorf("lists the command saw = %s, want the TUI's queued changes", got)
	}

	// The TUI changes a list again after the command wrote.
	if _, err := c.RenameList(ctx, trip.ID, "Holiday"); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(titles(t, again.Client()), ","); got != "From the command line" {
		t.Errorf("todos on disk = %s, want the command's", got)
	}
	if got := strings.Join(listNames(t, again.Client()), ","); got != "Holiday,Office,Personal" {
		t.Errorf("lists on disk = %s", got)
	}

	svc.down.Store(false)
	if _, err := tui.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	holiday := svc.named("Holiday")
	switch {
	case holiday == nil || svc.named("Office") == nil || svc.listAt("old") >= 0:
		t.Errorf("service lists = %+v, want Holiday made, Work renamed and Old deleted", svc.lists)
	case svc.todos[made[0].ID] == nil || svc.todos[made[0].ID].ListID != holiday.ID:
		t.Errorf("the command's todo on the service = %+v, want it in Holiday", svc.todos[made[0].ID])
	}
}
