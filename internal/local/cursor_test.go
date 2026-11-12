package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A file written before the service gave out cursors holds a time. The
// service refuses that as a cursor, so it is dropped and the next sync is a
// full pull, which keeps the todos here.
func TestATimeLeftAsACursorIsDroppedOnLoad(t *testing.T) {
	st := fresh(time.Now())
	st.Todos = []Todo{{Todo: wireTodo{}.Todo}}
	st.Cursor = "2026-03-10T15:00:00.123456789Z"
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "todos.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.st.Cursor != "" {
		t.Errorf("cursor = %q, want a time to be dropped", b.st.Cursor)
	}
	if len(b.st.Todos) != 1 {
		t.Errorf("todos = %d, dropping the cursor must keep what is here", len(b.st.Todos))
	}
}

func TestACursorTheServiceGaveIsKeptAsItCame(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := signedIn(t, svc, url)
	if !cursorShape.MatchString(b.st.Cursor) {
		t.Fatalf("cursor = %q, want what the service gave", b.st.Cursor)
	}
	first := b.st.Cursor
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if b.st.Cursor == first || !cursorShape.MatchString(b.st.Cursor) {
		t.Errorf("cursor = %q after a second sync, want the newer one the service gave", b.st.Cursor)
	}
}

// A first pull over several pages keeps its cursor out of the file until
// the last one. A cursor there says the account was read in full, so a
// pull that stopped part way, with the program or with a save that did not
// happen, would leave the next sync never pruning what the account no
// longer holds.
func TestAFirstPullSavesItsCursorOnlyAtTheEnd(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.put("kept", "In the account")
	b := open(t, "", "")
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	svc.moreSyncs = svc.syncs + 2
	svc.sinces, svc.cursors = nil, nil
	var saved []string
	svc.during = func() {
		st, err := load(b.path, time.Now())
		if err != nil {
			t.Error(err)
		}
		saved = append(saved, st.Cursor)
	}
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 3 {
		t.Fatalf("the file was read during %d pulls, want 3", len(saved))
	}
	for i, c := range saved {
		if c != "" {
			t.Errorf("cursor in the file during pull %d = %q, want none before the end", i+1, c)
		}
	}
	// Each page still starts where the one before it ended.
	if want := []string{"", svc.cursors[0], svc.cursors[1]}; !slices.Equal(svc.sinces, want) {
		t.Errorf("pulls started from %q, want %q", svc.sinces, want)
	}
	st, err := load(b.path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.Cursor == "" || st.Cursor != b.st.Cursor {
		t.Errorf("cursor in the file = %q, want the last one the service gave, %q", st.Cursor, b.st.Cursor)
	}

	// Later pulls start from it and save as they go, as before.
	svc.moreSyncs, saved = svc.syncs+1, nil
	first := st.Cursor
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[1] == "" || saved[1] == first {
		t.Errorf("cursors in the file during a later pull = %q, want the one each page gave", saved)
	}
}
