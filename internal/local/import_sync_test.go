package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

type importService struct {
	*service
	foreign      map[string]bool
	imports      int
	failAfter    bool
	hideReads    bool
	duringImport func()
	// refuse turns down the todos with these ids in the file, for the
	// reason given.
	refuse map[string]string
}

// The importer skips account ids atomically and copies inaccessible ids. The
// ordinary sync fixture still exercises subsequent edits and deletions.
func newImportService(t *testing.T) (*importService, string) {
	base, _ := newService(t)
	s := &importService{service: base, foreign: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/import" {
			if s.down.Load() {
				w.WriteHeader(503)
				return
			}
			s.imports++
			ex, err := api.ReadExport(r.Body)
			if err != nil {
				t.Errorf("import body: %v", err)
				w.WriteHeader(400)
				return
			}
			report := api.ImportResult{Complete: true, Refused: []api.ImportRefusal{}}
			for item, in := range ex.Todos {
				if why := s.refuse[in.ID]; why != "" {
					report.Refused = append(report.Refused, api.ImportRefusal{Item: item + 1, Kind: "todo", ID: in.ID, Reason: why})
					report.RefusedCount++
					continue
				}
				id := in.ID
				if s.foreign[id] {
					id = importedCopyID(s.user.ID, id)
				}
				if old := s.todos[id]; old != nil {
					report.Skipped++
					if old.DeletedAt != nil {
						report.SkippedDeleted++
					}
					continue
				}
				in.ID, in.ListID = id, r.URL.Query().Get("listId")
				in.CreatedAt, in.UpdatedAt = s.tick(), s.tick()
				if in.RemindAt != nil && (in.IsCompleted || !in.RemindAt.After(time.Now())) {
					in.RemindAt = nil
				}
				for i := range in.Checklist {
					in.Checklist[i].TodoID = id
					in.Checklist[i].ID = NewID()
				}
				s.todos[id] = &wireTodo{Todo: in.Todo, Checklist: in.Checklist}
				report.Added++
			}
			if s.duringImport != nil {
				fn := s.duringImport
				s.duringImport = nil
				fn()
			}
			if s.failAfter {
				s.failAfter = false
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(report)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/todos/") && r.Method == http.MethodGet {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/todos/"), "/")
			todo := s.todos[parts[0]]
			if s.hideReads || todo == nil || todo.DeletedAt != nil || s.foreign[parts[0]] {
				w.WriteHeader(404)
				return
			}
			if len(parts) == 2 && parts[1] == "checklist" {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": todo.Checklist})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"todo": todo.Todo})
			}
			return
		}
		s.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func importFixture(t *testing.T, b *Backend) api.Export {
	t.Helper()
	source := open(t, "", "")
	made := add(t, source.Client(), "File title")
	addSteps(t, source.Client(), made.ID, "Checklist")
	ex := source.Export()
	day := "2031-01-02"
	at := time.Date(2031, 1, 2, 9, 0, 0, 0, time.UTC)
	ex.Todos[0].ScheduledDay, ex.Todos[0].DoAt, ex.Todos[0].RemindAt = &day, &at, nil
	if _, err := b.Import(ex); err != nil {
		t.Fatal(err)
	}
	return ex
}

func TestImportedSyncSkipsExistingServerIDsAtEveryTimestamp(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(map[bool]string{false: "older file", true: "newer file"}[newer], func(t *testing.T) {
			s, url := newImportService(t)
			b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
			ex := importFixture(t, b)
			id := ex.Todos[0].ID
			s.put(id, "Server title")
			if newer {
				b.mu.Lock()
				b.find(id).UpdatedAt = s.todos[id].UpdatedAt.Add(time.Hour)
				_ = b.persist()
				b.mu.Unlock()
			}
			mustSync(t, b)
			if get(t, b.Client(), id).Title != "Server title" || s.todos[id].Title != "Server title" || s.pushes(id) != 0 {
				t.Fatal("import overwrote an existing server todo")
			}
		})
	}
}

func TestImportedForeignIDSyncRemapsOnceAndKeepsDeletionAlias(t *testing.T) {
	s, url := newImportService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	sourceID := ex.Todos[0].ID
	s.foreign[sourceID] = true
	mustSync(t, b)
	id := importedCopyID(s.user.ID, sourceID)
	got := get(t, b.Client(), id)
	if got.Title != "File title" || got.RemindAt != nil || s.pushes(sourceID) != 0 || s.pushes(id) != 0 || s.imports != 1 {
		t.Fatalf("foreign upload = %+v imports=%d", got, s.imports)
	}
	b, err := Open(b.path, "evt_x", url)
	if err != nil {
		t.Fatal(err)
	}
	if b.find(id).ImportedID != sourceID || b.st.ImportedIDs[sourceID] != id {
		t.Fatal("identity marker lost after pull/restart")
	}
	res, err := b.Import(ex)
	if err != nil || res.Skipped != 1 || res.Added != 0 {
		t.Fatalf("reimport = %+v %v", res, err)
	}
	if err := b.Client().DeleteTodo(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	mustSync(t, b)
	b, err = Open(b.path, "evt_x", url)
	if err != nil {
		t.Fatal(err)
	}
	res, err = b.Import(ex)
	if err != nil || res.SkippedDeleted != 1 || res.Added != 0 {
		t.Fatalf("deleted reimport = %+v %v", res, err)
	}
}

func TestImportedSyncFailureRetriesWithoutDuplicating(t *testing.T) {
	s, url := newImportService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	s.foreign[ex.Todos[0].ID] = true
	s.failAfter = true
	if _, err := b.Sync(context.Background()); err == nil {
		t.Fatal("failed import succeeded")
	}
	if !b.find(ex.Todos[0].ID).New {
		t.Fatal("failed import discarded retry state")
	}
	mustSync(t, b)
	if s.imports != 2 || len(s.todos) != 1 || b.Status().Pending != 0 {
		t.Fatalf("retry duplicated or unsettled data: imports=%d todos=%d pending=%d", s.imports, len(s.todos), b.Status().Pending)
	}
}

func TestImportedSyncKeepsEditsAndDeletesDuringUpload(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(map[bool]string{false: "edit", true: "delete"}[deleting], func(t *testing.T) {
			s, url := newImportService(t)
			b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
			ex := importFixture(t, b)
			sourceID := ex.Todos[0].ID
			s.foreign[sourceID] = true
			s.duringImport = func() {
				if deleting {
					if err := b.Client().DeleteTodo(context.Background(), sourceID); err != nil {
						t.Error(err)
					}
				} else {
					if _, _, err := b.Client().UpdateTodo(context.Background(), sourceID, api.TodoPatch{Title: api.Val("Edited during upload")}); err != nil {
						t.Error(err)
					}
				}
			}
			mustSync(t, b)
			id := importedCopyID(s.user.ID, sourceID)
			if deleting {
				if s.todos[id].DeletedAt == nil {
					t.Fatal("delete during import left server copy live")
				}
				res, err := b.Import(ex)
				if err != nil || res.SkippedDeleted != 1 {
					t.Fatalf("deletion alias = %+v %v", res, err)
				}
			} else if s.todos[id].Title != "Edited during upload" || get(t, b.Client(), id).Title != "Edited during upload" {
				t.Fatal("edit during import lost")
			}
		})
	}
}

func TestStoreMigratesLegacyVersionOnFirstWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	st := fresh(time.Now())
	st.Version = 1
	st.Todos = []Todo{{Todo: api.Todo{ID: NewID(), ListID: st.Lists[0].ID, Title: "Legacy", CreatedAt: time.Now(), UpdatedAt: time.Now()}, New: true}}
	raw, _ := json.Marshal(st)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.st.Version != version || b.st.Todos[0].Title != "Legacy" {
		t.Fatal("migration lost legacy data")
	}
	add(t, b.Client(), "New")
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != 2 || len(st.Todos) != 2 {
		t.Fatalf("migration save = %+v", st)
	}
	st.Version = 3
	raw, _ = json.Marshal(st)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "", ""); err == nil {
		t.Fatal("newer store accepted")
	}
}

func TestImportedTodoEditedBeforeSyncStillUsesImporter(t *testing.T) {
	s, url := newImportService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	id := ex.Todos[0].ID
	if _, _, err := b.Client().UpdateTodo(context.Background(), id, api.TodoPatch{Title: api.Val("Edited before upload")}); err != nil {
		t.Fatal(err)
	}
	if b.find(id).ImportedID != id {
		t.Fatal("editing lost import identity")
	}
	mustSync(t, b)
	if s.imports != 1 || s.pushes(id) != 0 || s.todos[id].Title != "Edited before upload" {
		t.Fatal("edited import used unsafe sync create")
	}
}

func TestImportedDeleteAfterFailedUploadRetriesDeletion(t *testing.T) {
	s, url := newImportService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	sourceID := ex.Todos[0].ID
	s.foreign[sourceID] = true
	s.failAfter = true
	s.duringImport = func() {
		if err := b.Client().DeleteTodo(context.Background(), sourceID); err != nil {
			t.Error(err)
		}
	}
	if _, err := b.Sync(context.Background()); err == nil {
		t.Fatal("failed request succeeded")
	}
	b, err := Open(b.path, "evt_x", url)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.st.Todos) != 1 || b.st.Todos[0].DeletedAt == nil || !b.st.Todos[0].ImportStarted {
		t.Fatal("unresolved deletion lost across restart")
	}
	mustSync(t, b)
	id := importedCopyID(s.user.ID, sourceID)
	if s.todos[id].DeletedAt == nil {
		t.Fatal("failed upload left remotely created copy live")
	}
	res, err := b.Import(ex)
	if err != nil || res.SkippedDeleted != 1 {
		t.Fatalf("deleted alias %+v %v", res, err)
	}
}

// An imported todo deleted here while its upload was on the way, which the
// service then turned down, is not in the account and is not wanted here.
// It goes, rather than going up again on every sync.
func TestRefusedImportDeletedDuringUploadIsNotSentAgain(t *testing.T) {
	s, url := newImportService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	sourceID := ex.Todos[0].ID
	s.refuse = map[string]string{sourceID: "too many todos"}
	s.duringImport = func() {
		if err := b.Client().DeleteTodo(context.Background(), sourceID); err != nil {
			t.Error(err)
		}
	}
	if res := mustSync(t, b); len(res.Refused) != 0 {
		t.Errorf("refused = %v, want nothing reported for a todo deleted here", res.Refused)
	}
	mustSync(t, b)
	if s.imports != 1 {
		t.Errorf("imports = %d, want the refused todo sent once", s.imports)
	}
	if st := b.Status(); st.Pending != 0 || st.Refused != 0 {
		t.Errorf("status = %+v, want nothing pending or refused", st)
	}
	if len(b.st.Todos) != 0 {
		t.Errorf("todos here = %+v, want none", b.st.Todos)
	}
	if res, err := b.Import(ex); err != nil || res.Added != 0 {
		t.Errorf("importing the file again = %+v %v, want the deletion kept", res, err)
	}
}

func TestUnsyncedCaptureDeletionCannotBeRestoredByImport(t *testing.T) {
	b := open(t, "", "")
	todo := add(t, b.Client(), "Deleted here")
	ex := b.Export()
	if err := b.Client().DeleteTodo(context.Background(), todo.ID); err != nil {
		t.Fatal(err)
	}
	b, err := Open(b.path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Import(ex)
	if err != nil || res.Added != 0 || res.SkippedDeleted != 1 {
		t.Fatalf("offline deletion restored: %+v %v", res, err)
	}
}

func TestImportedNewTodoWaitsWhenServerHasNoImporter(t *testing.T) {
	s, url := newService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	if _, err := b.Sync(context.Background()); err == nil {
		t.Fatal("old server import unexpectedly succeeded")
	}
	if s.pushes(ex.Todos[0].ID) != 0 || !b.find(ex.Todos[0].ID).New {
		t.Fatal("old server fell back to unsafe sync create")
	}
}

func TestImportedServerDeletedIDRemainsDeleted(t *testing.T) {
	s, url := newImportService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	id := ex.Todos[0].ID
	s.put(id, "Deleted server todo")
	now := s.tick()
	s.todos[id].DeletedAt = &now
	mustSync(t, b)
	if b.find(id) != nil || s.todos[id].DeletedAt == nil {
		t.Fatal("server deletion was restored")
	}
	res, err := b.Import(ex)
	if err != nil || res.SkippedDeleted != 1 || res.Added != 0 {
		t.Fatalf("deleted server alias: %+v %v", res, err)
	}
}

func TestImportedAddedRecordMissingFromReadsRemainsPending(t *testing.T) {
	s, url := newImportService(t)
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	ex := importFixture(t, b)
	s.hideReads = true
	if _, err := b.Sync(context.Background()); err == nil {
		t.Fatal("unresolved added todo was silently dropped")
	}
	if cur := b.find(ex.Todos[0].ID); cur == nil || !cur.New {
		t.Fatal("unresolved import lost local copy")
	}
	s.hideReads = false
	mustSync(t, b)
	if len(s.todos) != 1 || b.Status().Pending != 0 {
		t.Fatal("unresolved import failed idempotent retry")
	}
}

func TestImportedRetryPreservesEditsAfterAmbiguousResponse(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "same process", true: "restart"}[restart], func(t *testing.T) {
			s, url := newImportService(t)
			b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
			ex := importFixture(t, b)
			sourceID := ex.Todos[0].ID
			sourceStep := ex.Todos[0].Checklist[0].ID
			s.foreign[sourceID] = true
			s.failAfter = true
			s.duringImport = func() {
				if _, _, err := b.Client().UpdateTodo(context.Background(), sourceID, api.TodoPatch{Title: api.Val("Edited while upload failed")}); err != nil {
					t.Error(err)
				}
				title := "Checklist edited during failure"
				if _, err := b.Client().UpdateChecklistItem(context.Background(), sourceStep, &title, nil); err != nil {
					t.Error(err)
				}
			}
			if _, err := b.Sync(context.Background()); err == nil {
				t.Fatal("ambiguous response succeeded")
			}
			if restart {
				var err error
				b, err = Open(b.path, "evt_x", url)
				if err != nil {
					t.Fatal(err)
				}
			}
			notes := "Edited after failed request"
			due := time.Date(2032, 3, 4, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
			if _, _, err := b.Client().UpdateTodo(context.Background(), sourceID, api.TodoPatch{Notes: api.Val(notes), DueAt: api.Val(due)}); err != nil {
				t.Fatal(err)
			}
			mustSync(t, b)
			id := importedCopyID(s.user.ID, sourceID)
			got := get(t, b.Client(), id)
			steps := checklist(t, b.Client(), id)
			if got.Title != "Edited while upload failed" || got.Notes == nil || *got.Notes != notes || got.DueAt == nil || got.DueAt.Year() != 2032 || len(steps) != 1 || steps[0].Title != "Checklist edited during failure" {
				t.Fatalf("edits lost after retry: %+v %+v", got, steps)
			}
			if steps[0].ID == sourceStep {
				t.Fatal("retry exposed foreign source step id to generic sync")
			}
			if s.imports != 2 || len(s.todos) != 1 || b.Status().Pending != 0 {
				t.Fatal("retry duplicated data or did not settle")
			}
		})
	}
}

func TestImportedResolvedCopyDeduplicatesExistingLocalTarget(t *testing.T) {
	for _, state := range []string{"live", "edited", "deleted"} {
		t.Run(state, func(t *testing.T) {
			s, url := newImportService(t)
			b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
			source := open(t, "", "")
			original := add(t, source.Client(), "File title")
			ex := source.Export()
			id := importedCopyID(s.user.ID, original.ID)
			s.foreign[original.ID] = true
			s.put(id, "Existing copy")
			mustSync(t, b)
			if b.find(id).ImportedID != "" {
				t.Fatal("fixture already has a source alias")
			}
			if _, err := b.Import(ex); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "edited":
				if _, _, err := b.Client().UpdateTodo(context.Background(), id, api.TodoPatch{Title: api.Val("Existing local edit")}); err != nil {
					t.Fatal(err)
				}
				s.duringImport = func() {
					if _, _, err := b.Client().UpdateTodo(context.Background(), original.ID, api.TodoPatch{Title: api.Val("Concurrent placeholder edit"), Notes: api.Val("Placeholder notes")}); err != nil {
						t.Error(err)
					}
				}
			case "deleted":
				if err := b.Client().DeleteTodo(context.Background(), id); err != nil {
					t.Fatal(err)
				}
			}
			mustSync(t, b)
			count := 0
			for _, todo := range b.st.Todos {
				if todo.ID == id {
					count++
				}
				if todo.ID == original.ID {
					t.Fatal("source placeholder remained")
				}
			}
			if count > 1 {
				t.Fatal("duplicate resolved ids")
			}
			if state == "deleted" {
				if s.todos[id].DeletedAt == nil {
					t.Fatal("existing target deletion lost")
				}
			} else {
				if count != 1 {
					t.Fatal("existing target missing")
				}
				want := "Existing copy"
				if state == "edited" {
					want = "Existing local edit"
				}
				got := get(t, b.Client(), id)
				if got.Title != want {
					t.Fatalf("target changed to %q", got.Title)
				}
				if state == "edited" && (got.Notes == nil || *got.Notes != "Placeholder notes") {
					t.Fatal("nonconflicting placeholder edit lost")
				}
			}
			res, err := b.Import(ex)
			if err != nil || res.Skipped != 1 || res.Added != 0 {
				t.Fatalf("deduplicated reimport=%+v %v", res, err)
			}
		})
	}
}
