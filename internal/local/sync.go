package local

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// Result is what one sync did.
type Result struct {
	// Pushed is how many todos were sent, and Pulled how many came back.
	Pushed, Pulled int
	// Changed reports that what is kept here is different afterwards, so
	// anything showing it should look again.
	Changed bool
	// Refused is what the service turned down, by todo title.
	Refused []string
	// ListsRefused says, for each list change the service turned down and
	// that was undone here, which list and why.
	ListsRefused []string
	// ListsWaiting names deletions held until moves reach their target lists.
	ListsWaiting []string
	// InvitationsChanged reports that the pending invitations are different
	// afterwards.
	InvitationsChanged bool
	// Replaced is each todo with a change made here that gave way to a
	// newer change to the same field from the service, or to the service
	// deleting the todo, once for each todo.
	Replaced []Replacement
	// ListsGone says, for each list that left the account while todos with
	// changes the service had not heard were kept here in it, what became of
	// them. A list that left with nothing unsent in it is not here.
	ListsGone []GoneList
}

// GoneList is a list the account no longer has, and what became of the
// todos with unsent changes that were kept here in it.
type GoneList struct {
	// Name is the list's name as it was shown here.
	Name string
	// Moved are the titles of todos made here that were moved to the
	// personal list, which Into names, so they still go up.
	Moved []string
	Into  string
	// Lost are the titles of todos made here that went with the list because
	// the personal list could not be found to move them to.
	Lost []string
	// Edits are the titles of todos the account held whose unsent changes
	// were lost, because the todo went with the list or because the change
	// was a move into it, which is undone.
	Edits []string
}

// Replacement is a todo with changes made here that the service replaced.
type Replacement struct {
	ID, Title string
	// Fields names the fields that were replaced, as the interface does.
	Fields []string
	// Deleted means the service deleted the todo, which wins over any
	// change made to it here.
	Deleted bool
}

// String is the todo and what was replaced, for a person to read.
func (r Replacement) String() string {
	what := strings.Join(r.Fields, ", ")
	if r.Deleted {
		what = "deleted"
	}
	return fmt.Sprintf("'%s' (%s)", r.Title, what)
}

// replaced adds what a pull replaced, merged with what an earlier one in
// the same sync did to the same todo.
func (r *Result) replaced(more []Replacement) {
	for _, m := range more {
		i := slices.IndexFunc(r.Replaced, func(x Replacement) bool { return x.ID == m.ID })
		if i < 0 {
			r.Replaced = append(r.Replaced, m)
			continue
		}
		x := &r.Replaced[i]
		x.Deleted = x.Deleted || m.Deleted
		for _, f := range m.Fields {
			if !slices.Contains(x.Fields, f) {
				x.Fields = append(x.Fields, f)
			}
		}
	}
}

// fieldNames are the names the interface gives the fields of a todo.
var fieldNames = map[string]string{
	fTitle: "title", fNotes: "notes", fList: "list", fAssignee: "assignee",
	fDo: "do date", fDue: "due", fRemind: "reminder", fRule: "repeat",
	fCompleted: "done", fSort: "order", fDeleted: "deleted", fChecklist: "checklist",
}

// wire shapes of the /sync answer, which carries more than the client's
// own types keep: each todo's checklist, and each list's members.
type (
	wireTodo struct {
		api.Todo
		Checklist []api.ChecklistItem `json:"checklist"`
	}
	wireList struct {
		api.List
		Members []api.Member `json:"members"`
	}
	syncAnswer struct {
		Todos []wireTodo `json:"todos"`
		Lists []wireList `json:"lists"`
		// Invitations is a full snapshot. Absent leaves what is kept; an
		// empty array says there are none.
		Invitations []api.Invitation `json:"invitations"`
		Cursor      string           `json:"cursor"`
		HasMore     bool             `json:"hasMore"`
		Errors      []api.SyncError  `json:"errors"`
		// Deferred is the changes the service did not get to: not applied,
		// not refused, to be sent again. See splitDeferred.
		Deferred []api.DeferredChange `json:"deferred"`
	}
)

// ErrIncomplete is what a sync reports when it stopped at its round limit
// with more still to fetch or send. What it did get is kept, and the next
// sync carries on.
var ErrIncomplete = errors.New("sync stopped before everything was exchanged; it will carry on next time")

// pushBatch is the most changes the service takes in one call, and
// maxRounds bounds how long one sync can run. A call the service cuts short
// (see splitDeferred) sends the rest in the next round, so a large backlog uses the
// rounds too.
//
// maxPushes bounds how often one sync sends a todo. An edit skipped for a
// newer version of the service's goes again once merged, and again if it
// loses another such race; past that, a todo the service keeps answering
// with something other than what was sent, as it would if it stored a
// value differently, waits for the next sync instead of using up the
// rounds.
const (
	pushBatch = 200
	maxRounds = 60
	maxPushes = 3
)

// Sync sends what the service has not heard and fetches what this computer
// has not seen. With no account it does nothing. Conflicts are settled
// field by field: a field changed on one side only keeps that change, and
// one changed on both goes to the change made last. See merge.
//
// The network is never touched while the lock is held, so the todos stay
// usable during a slow sync. A todo edited while its push is in the air
// keeps the edit, which goes up on the next sync.
func (b *Backend) Sync(ctx context.Context) (Result, error) {
	b.mu.Lock()
	if b.remote == nil || b.st.UserID == "" || b.syncing {
		b.mu.Unlock()
		return Result{}, nil
	}
	b.syncing = true
	remote := b.remote
	b.mu.Unlock()

	res, err := b.sync(ctx, remote)

	b.mu.Lock()
	b.syncing = false
	b.lastErr = err
	if err == nil {
		now := b.now()
		b.st.LastSync = &now
	}
	saveErr := b.persist()
	b.mu.Unlock()
	if err == nil {
		err = saveErr
	}
	return res, err
}

func (b *Backend) sync(ctx context.Context, remote *api.Client) (Result, error) {
	var res Result

	// The account first: it says whether the token still works, and
	// carries the timezone every date here is read in.
	user, _, err := remote.Me(ctx)
	if err != nil {
		return res, err
	}
	b.mu.Lock()
	if user.ID != b.st.UserID {
		b.mu.Unlock()
		return res, &api.Error{StatusCode: http.StatusForbidden, Code: "forbidden", Message: "the token belongs to a different account than the todos on this computer"}
	}
	if b.st.User.TZ != user.TZ || !sameName(b.st.User.Username, user.Username) {
		res.Changed = true
	}
	b.st.User = user
	b.nameSelf()
	for i := range b.st.Todos {
		b.st.Todos[i].ScheduledDay = b.dayOf(b.st.Todos[i].DoAt)
	}
	// Each section of a sync that changes the state saves it before letting
	// go, as everything holding the lock does: another copy of evado may
	// write the file before the next one, and this backend then reads it
	// again. See disk.go.
	err = b.persist()
	b.mu.Unlock()
	if err != nil {
		return res, err
	}

	// Lists before todos: a todo names its list, so a list made here has
	// to exist in the account before its todos can go up. Deletions come
	// after them instead, so a todo moved out of a list that is then
	// deleted reaches its new list before the service deletes the old one.
	// A list is not deleted while a move out of it is unsettled or was
	// refused; see deleteLists.
	refused, err := b.makeLists(ctx, remote, &res)
	if err != nil {
		return res, err
	}
	if err := b.renameLists(ctx, remote, &res); err != nil {
		return res, err
	}
	if err := b.uploadImports(ctx, remote, &res); err != nil {
		return res, errors.Join(refused, err)
	}
	if err := b.exchange(ctx, remote, &res); err != nil {
		return res, errors.Join(refused, err)
	}
	deleted, undone, err := b.deleteLists(ctx, remote, &res)
	if err != nil {
		return res, errors.Join(refused, err)
	}
	// A deletion the service turned down brings the list back, with the
	// todos it still holds in it, which only a full pull fetches. A list
	// that could not be made may have been over the limit until a deletion
	// made room. Either way one more pass settles it now.
	if undone || deleted && refused != nil {
		if refused, err = b.makeLists(ctx, remote, &res); err != nil {
			return res, err
		}
		if err := b.exchange(ctx, remote, &res); err != nil {
			return res, errors.Join(refused, err)
		}
	}
	return res, refused
}

// refusal reports whether an error is the service turning a change down
// for good, as opposed to a failure worth trying again: no connection, a
// rate limit, a server error, or a token it no longer takes.
func refusal(err error) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && !apiErr.Retryable() && apiErr.StatusCode != http.StatusUnauthorized
}

// uploadImports uses the atomic importer for first uploads, because /sync edits
// an existing id by timestamp and could replace a todo that was absent locally.
func (b *Backend) uploadImports(ctx context.Context, remote *api.Client, res *Result) error {
	b.mu.Lock()
	userID := b.st.UserID
	groups := map[string][]Todo{}
	var order []string
	for _, t := range b.st.Todos {
		l := b.list(t.ListID)
		if !t.New || t.ImportedID == "" || !t.pending() || t.DeletedAt != nil && !t.ImportStarted || l == nil || l.Local {
			continue
		}
		if _, exists := groups[t.ListID]; !exists {
			order = append(order, t.ListID)
		}
		t.Steps = slices.Clone(t.Steps)
		groups[t.ListID] = append(groups[t.ListID], t)
	}
	b.mu.Unlock()
	for _, listID := range order {
		group := groups[listID]
		for start := 0; start < len(group); start += 100 {
			batch := group[start:min(start+100, len(group))]
			b.mu.Lock()
			starting := map[string]int{}
			for i, sent := range batch {
				starting[sent.ID] = i
			}
			for i := range b.st.Todos {
				if j, present := starting[b.st.Todos[i].ID]; present {
					cur := &b.st.Todos[i]
					if cur.ImportBase == nil {
						cur.ImportBase = &Todo{Todo: batch[j].Todo, Steps: slices.Clone(batch[j].Steps)}
					}
					cur.ImportStarted = true
					batch[j].ImportBase = cur.ImportBase
				}
			}
			err := b.persist()
			b.mu.Unlock()
			if err != nil {
				return err
			}
			ex := api.Export{Lists: []api.ListSnapshot{}, Todos: []api.SyncTodo{}}
			for _, t := range batch {
				if t.ImportBase != nil {
					t = *t.ImportBase
				}
				todo := t.Todo
				todo.DeletedAt = nil
				ex.Todos = append(ex.Todos, api.SyncTodo{Todo: todo, Checklist: t.Steps})
			}
			var buf bytes.Buffer
			if err := api.EncodeTodosExport(&buf, ex); err != nil {
				return err
			}
			report, err := remote.Import(ctx, buf.Bytes(), api.ImportOptions{ListID: listID})
			if err != nil {
				return err
			}
			if !report.Complete {
				return ErrIncomplete
			}
			refused := map[int]string{}
			for _, item := range report.Refused {
				refused[item.Item] = item.Reason
			}
			res.Pushed += report.Added
			deletions := report.SkippedDeleted
			for i, sent := range batch {
				if reason := refused[i+1]; reason != "" {
					b.mu.Lock()
					index := slices.IndexFunc(b.st.Todos, func(t Todo) bool {
						return t.ID == sent.ID && t.New && t.ImportedID == sent.ImportedID
					})
					if index >= 0 {
						if cur := &b.st.Todos[index]; cur.DeletedAt != nil {
							// Deleted here while it waited, and the service did not
							// take it, so there is nothing left to tell. Kept, it
							// would go up and be refused again on every sync.
							b.drop(cur.ID)
						} else {
							cur.Refused = reason
							res.Refused = append(res.Refused, cur.Title)
						}
					}
					err := b.persist()
					b.mu.Unlock()
					if err != nil {
						return err
					}
					continue
				}
				id := sent.ID
				todo, err := remote.GetTodo(ctx, id)
				if isMissing(err) {
					id = importedCopyID(userID, sent.ImportedID)
					todo, err = remote.GetTodo(ctx, id)
				}
				deleted := isMissing(err)
				if err != nil && !deleted {
					return err
				}
				if deleted {
					// A batch report has no per-item identity map. A missing added row must
					// remain pending instead of being mistaken for another row's deletion.
					if report.Added > 0 || deletions == 0 {
						return fmt.Errorf("could not resolve imported todo %s; its local copy remains pending", sent.ID)
					}
					deletions--
				}
				deleted = deleted || todo.Tombstoned()
				var steps []api.ChecklistItem
				if !deleted {
					steps, err = remote.ListChecklist(ctx, id)
					if isMissing(err) {
						return fmt.Errorf("could not resolve imported checklist for %s; its local copy remains pending", sent.ID)
					} else if err != nil {
						return err
					}
				}
				b.mu.Lock()
				if b.st.UserID != userID {
					b.mu.Unlock()
					return fmt.Errorf("the account changed while imported todos were uploading")
				}
				if b.st.ImportedIDs == nil {
					b.st.ImportedIDs = map[string]string{}
				}
				b.st.ImportedIDs[sent.ImportedID] = id
				index := slices.IndexFunc(b.st.Todos, func(t Todo) bool { return t.ID == sent.ID })
				if deleted {
					if index >= 0 {
						b.st.Todos = slices.Delete(b.st.Todos, index, index+1)
					}
				} else {
					in := Todo{Todo: todo, Steps: steps, ImportedID: sent.ImportedID, Spawned: sent.Spawned}
					for j := range in.Steps {
						in.Steps[j].TodoID = in.ID
					}
					targetIndex := -1
					if id != sent.ID {
						targetIndex = slices.IndexFunc(b.st.Todos, func(t Todo) bool { return t.ID == id })
					}
					if targetIndex >= 0 {
						target := b.st.Todos[targetIndex]
						// The resolved copy may already be on this computer from another
						// device. Its pending edits and deletion take precedence over the file.
						if target.DeletedAt == nil && !target.New {
							base := in.based()
							compared := sent
							if sent.ImportBase != nil {
								compared = *sent.ImportBase
							}
							if index >= 0 {
								cur := b.st.Todos[index]
								for _, field := range []string{fTitle, fNotes, fList, fAssignee, fDo, fDue, fRemind, fRule, fCompleted, fSort, fChecklist, fDeleted} {
									if !contains(target.Dirty, field) && !same(field, &cur, &compared) {
										carry(field, &in, &cur)
										in.Dirty = append(in.Dirty, field)
									}
								}
							}
							for _, field := range target.Dirty {
								carry(field, &in, &target)
								if !contains(in.Dirty, field) {
									in.Dirty = append(in.Dirty, field)
								}
							}
							if contains(in.Dirty, fChecklist) && !contains(target.Dirty, fChecklist) {
								in.Steps = slices.Clone(in.Steps)
								for j := range in.Steps {
									in.Steps[j].ID = NewID()
									in.Steps[j].TodoID = in.ID
								}
							}
							if len(in.Dirty) > 0 {
								in.Base = base
								in.UpdatedAt = after(in.UpdatedAt, b.now())
								in.UpdatedAt = after(in.UpdatedAt, target.UpdatedAt)
							}
							if target.ImportedID != "" {
								in.ImportedID = target.ImportedID
							}
							b.st.Todos[targetIndex] = in
						} else if target.ImportedID == "" {
							b.st.Todos[targetIndex].ImportedID = sent.ImportedID
						}
						if index >= 0 {
							b.st.Todos = slices.Delete(b.st.Todos, index, index+1)
						}
					} else if index < 0 {
						// A delete while the request was in flight must delete the uploaded copy too.
						base := in.based()
						now := b.now()
						in.Base = base
						in.DeletedAt = &now
						in.touch(now, fDeleted)
						b.st.Todos = append(b.st.Todos, in)
					} else {
						cur := b.st.Todos[index]
						compared := sent
						if sent.ImportBase != nil {
							compared = *sent.ImportBase
						}
						base := in.based()
						for _, field := range []string{fTitle, fNotes, fList, fAssignee, fDo, fDue, fRemind, fRule, fCompleted, fSort, fChecklist, fDeleted} {
							if !same(field, &cur, &compared) || field == fDeleted && cur.DeletedAt != nil {
								carry(field, &in, &cur)
								in.Dirty = append(in.Dirty, field)
							}
						}
						if contains(in.Dirty, fChecklist) {
							in.Steps = slices.Clone(in.Steps)
							copied := map[string]bool{}
							for _, step := range compared.Steps {
								copied[step.ID] = true
							}
							// The importer creates fresh checklist ids. A concurrent local edit
							// must not send the source account's step ids back through /sync.
							for j := range in.Steps {
								if copied[in.Steps[j].ID] {
									in.Steps[j].ID = NewID()
								}
							}
						}
						if len(in.Dirty) > 0 {
							in.Base = base
							in.UpdatedAt = after(in.UpdatedAt, cur.UpdatedAt)
						}
						for j := range in.Steps {
							in.Steps[j].TodoID = in.ID
						}
						b.st.Todos[index] = in
					}
				}
				res.Changed = true
				err = b.persist()
				b.mu.Unlock()
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func isMissing(err error) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// This is the server importer's stable identity for a foreign-account copy.
func importedCopyID(userID, sourceID string) string {
	sum := sha256.Sum256([]byte("evado-import:" + userID + ":" + sourceID))
	h := fmt.Sprintf("%x", sum)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

// makeLists creates in the account the lists made here, oldest first. A
// list the service turns down, for instance for too many lists, keeps its
// todos pending and is tried again next sync; the refusals come back
// together so the rest can go on.
func (b *Backend) makeLists(ctx context.Context, remote *api.Client, res *Result) (error, error) {
	b.mu.Lock()
	var unmade []List
	for _, l := range b.st.Lists {
		if l.Local {
			unmade = append(unmade, l)
		}
	}
	b.mu.Unlock()

	var refused error
	for _, l := range unmade {
		made, err := remote.CreateList(ctx, l.Name)
		if err != nil {
			if !refusal(err) {
				return nil, err // the error keeps the service's retry delay
			}
			refused = errors.Join(refused, fmt.Errorf("could not create the list %q: %w", l.Name, err))
			continue
		}
		b.mu.Lock()
		b.bindList(l.ID, l.Name, made)
		err = b.persist()
		b.mu.Unlock()
		if err != nil {
			// The list now exists in the account, and without its id kept
			// here the next sync would make it a second time.
			return refused, err
		}
		res.Changed = true
	}
	return refused, nil
}

// renameLists sends the names given here to lists the account holds. A
// rename the service turns down is undone and reported.
func (b *Backend) renameLists(ctx context.Context, remote *api.Client, res *Result) error {
	b.mu.Lock()
	var renamed []List
	for _, l := range b.st.Lists {
		if l.Renamed && !l.Local {
			renamed = append(renamed, l)
		}
	}
	b.mu.Unlock()

	for _, l := range renamed {
		_, err := remote.RenameList(ctx, l.ID, l.Name)
		if err != nil && !refusal(err) {
			return err
		}
		b.mu.Lock()
		cur := b.list(l.ID)
		switch {
		case cur == nil:
			// Deleted here meanwhile, which needs no name.
		case err != nil && cur.Renamed:
			res.ListsRefused = append(res.ListsRefused, fmt.Sprintf("renaming %q to %q: %s", cur.Was, cur.Name, reason(err)))
			cur.Name, cur.Renamed, cur.Was = cur.Was, false, ""
			res.Changed = true
		case err != nil:
			// Renamed back meanwhile to the name the service kept.
		case cur.Name == l.Name:
			cur.Renamed, cur.Was = false, ""
		default:
			// Renamed again while this one was on its way, back to the
			// old name included; the service now has the one sent.
			cur.Renamed, cur.Was = true, l.Name
		}
		err = b.persist()
		b.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// deleteLists deletes in the account the lists deleted here, in order. It
// reports whether any went, and whether any deletion was turned down and
// undone: the list is then left for the pull to bring back, as the service
// still has it.
//
// A list is not deleted while a todo moved out of it has not left it on the
// service, for the service would delete the todo with the list. If the
// service turned that move down the deletion is undone, as if it had been
// turned down itself; if the move is only waiting, as when the list it goes
// to has not been made yet, the list stays queued for the next sync.
func (b *Backend) deleteLists(ctx context.Context, remote *api.Client, res *Result) (deleted, undone bool, err error) {
	b.mu.Lock()
	queued := append([]List(nil), b.st.Deleted...)
	b.mu.Unlock()

	for _, l := range queued {
		b.mu.Lock()
		stuck, waiting := b.movesOutOf(l.ID)
		if waiting && len(stuck) == 0 {
			var targets []string
			for _, t := range b.st.Todos {
				if t.Base != nil && t.Base.ListID == l.ID && t.ListID != l.ID && t.DeletedAt == nil {
					name := t.ListID
					if target := b.list(t.ListID); target != nil {
						name = target.Name
					}
					if !slices.Contains(targets, name) {
						targets = append(targets, name)
					}
				}
			}
			res.ListsWaiting = append(res.ListsWaiting, fmt.Sprintf("deleting %q waits for moves to %s to sync", l.Name, strings.Join(targets, ", ")))
		}
		b.mu.Unlock()
		if waiting && len(stuck) == 0 {
			continue
		}
		var err error
		gone := false
		if len(stuck) == 0 {
			err = remote.DeleteList(ctx, l.ID)
			var apiErr *api.Error
			// Not found is a list someone else deleted first, which is done.
			gone = err == nil || errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
			if !gone && !refusal(err) {
				return deleted, undone, err
			}
		}
		b.mu.Lock()
		kept := b.st.Deleted[:0]
		for _, x := range b.st.Deleted {
			if x.ID != l.ID {
				kept = append(kept, x)
			}
		}
		b.st.Deleted = kept
		if gone {
			deleted = true
		} else {
			var why string
			if len(stuck) > 0 {
				why = strings.Join(stuck, ", ") + " could not be moved out of it"
			} else {
				why = reason(err)
			}
			res.ListsRefused = append(res.ListsRefused, fmt.Sprintf("deleting %q: %s", l.Name, why))
			// Its todos were dropped here when it was deleted, and an
			// incremental pull only brings what changed, so the next one
			// reads the account in full.
			b.st.Cursor = ""
			undone = true
		}
		err = b.persist()
		b.mu.Unlock()
		if err != nil {
			// Kept in memory only, the deletion would be read back as still
			// queued and sent again, which the service answers as done.
			return deleted, undone, err
		}
		res.Changed = true
	}
	return deleted, undone, nil
}

// movesOutOf finds the todos moved out of a list that the service still has
// in it: stuck are the titles, in quotes, of those whose move it turned
// down, and waiting reports whether any move has yet to be settled. A todo
// deleted here is left out, as the deletion takes it anyway. The caller
// holds the lock.
func (b *Backend) movesOutOf(listID string) (stuck []string, waiting bool) {
	for i := range b.st.Todos {
		t := &b.st.Todos[i]
		if t.DeletedAt != nil || t.Base == nil || t.Base.ListID != listID || t.ListID == listID || !contains(t.Dirty, fList) {
			continue
		}
		waiting = true
		if t.Refused != "" {
			stuck = append(stuck, "'"+t.Title+"'")
		}
	}
	return stuck, waiting
}

// reason is what the service said about a refusal, without the code.
func reason(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}

// exchange pushes the pending todos and pulls what changed, in rounds,
// until there is nothing left either way.
func (b *Backend) exchange(ctx context.Context, remote *api.Client, res *Result) error {
	initial, complete := false, false
	seen := map[string]bool{}
	var turnedDown []string
	b.mu.Lock()
	for _, t := range b.st.Todos {
		if t.New && t.ImportedID != "" && t.Refused != "" {
			turnedDown = append(turnedDown, t.ID)
		}
	}
	b.awaiting, b.pushes = map[string]Todo{}, map[string]int{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		// Edits still awaiting a pull are pending in the file, and go up
		// again next time.
		b.awaiting, b.pushes = nil, nil
		// A first pull that did not finish leaves the cursor where it got
		// to, and the next sync would take that for an account already read
		// in full and never prune. Starting over is the only way to know
		// which todos the account really holds.
		if initial && !complete {
			b.st.Cursor = ""
			_ = b.persist()
		}
		b.mu.Unlock()
	}()
	// stalled means a round got nothing applied because the service
	// deferred every change in it. Sending them again would get the same
	// answer, so the rest of this sync only pulls, and the next one tries
	// again.
	stalled := false
	for round := 0; round < maxRounds; round++ {
		b.mu.Lock()
		changes, sent := []map[string]any{}, map[string]Todo{}
		if !stalled {
			changes, sent = b.changes()
		}
		since := b.st.Cursor
		b.mu.Unlock()
		if round == 0 {
			initial = since == ""
		}

		// acceptDeferred makes a service that cuts a large push short say
		// which changes it left, instead of reporting them as errors. One
		// that does not know the field ignores it.
		body := map[string]any{"changes": changes, "acceptDeferred": true}
		if since != "" {
			body["since"] = since
		}
		var ans syncAnswer
		if err := remote.Call(ctx, http.MethodPost, "/sync", body, &ans); err != nil {
			return err
		}

		b.mu.Lock()
		refusals, unapplied := splitDeferred(sent, ans)
		turnedDown = append(turnedDown, b.settle(sent, refusals, unapplied)...)
		res.Pushed += len(sent) - len(refusals) - len(unapplied)
		stalled = stalled || len(sent) > 0 && len(unapplied) == len(sent)
		changed, replaced, left := b.apply(ans, seen)
		if changed {
			res.Changed = true
		}
		res.replaced(replaced)
		res.ListsGone = append(res.ListsGone, left...)
		// The pull can mend a refused change, which then goes again.
		res.Refused = b.stillRefused(turnedDown)
		if b.applyInvitations(ans.Invitations) {
			res.InvitationsChanged = true
		}
		res.Pulled += len(ans.Todos)
		if ans.Cursor != "" {
			b.st.Cursor = ans.Cursor
		}
		more := ans.HasMore || !stalled && b.hasPending()
		err := b.persist()
		b.mu.Unlock()
		if err != nil {
			return err
		}
		if !more {
			complete = true
			break
		}
	}
	if !complete {
		return ErrIncomplete
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.confirm()
	if initial {
		// A first pull is everything the account holds, so a todo here
		// that the service knows about and did not send is gone there.
		kept := b.st.Todos[:0]
		for _, t := range b.st.Todos {
			if seen[t.ID] || t.New || len(t.Dirty) > 0 {
				kept = append(kept, t)
			} else {
				res.Changed = true
			}
		}
		b.st.Todos = kept
	}
	if err := b.persist(); err != nil {
		return err
	}
	if stalled {
		return ErrIncomplete
	}
	return nil
}

// splitDeferred splits what the service said about the changes sent into the
// refusals and the ids it left unapplied. A deferral is not a refusal: the
// change was never tried, so it stays pending and goes again. The service
// lists them in deferred; one it put in errors instead is told by its code
// or message. Ids that were not in this push are ignored, and so is a
// refusal of one also left unapplied, which settle keeps pending; each
// refused id is there once, with the last reason given, so the refusals
// can be counted against the push.
func splitDeferred(sent map[string]Todo, ans syncAnswer) (refusals []api.SyncError, left map[string]bool) {
	left = map[string]bool{}
	for _, d := range ans.Deferred {
		if _, ok := sent[d.ID]; ok {
			left[d.ID] = true
		}
	}
	for _, e := range ans.Errors {
		if _, ok := sent[e.ID]; ok && e.Deferred() {
			left[e.ID] = true
		}
	}
	at := map[string]int{}
	for _, e := range ans.Errors {
		if _, ok := sent[e.ID]; !ok || e.Deferred() || left[e.ID] {
			continue
		}
		if i, again := at[e.ID]; again {
			refusals[i] = e
			continue
		}
		at[e.ID] = len(refusals)
		refusals = append(refusals, e)
	}
	return refusals, left
}

// confirm takes the edits still awaiting a pull for applied, once the pull
// has been read to the end. The change feed carries every change after the
// cursor, so one the service skipped for a newer version of its own would
// have come back; an applied one can go unseen, as a deletion does in a
// first pull, which only holds live todos. The caller holds the lock.
func (b *Backend) confirm() {
	kept := b.st.Todos[:0]
	for _, t := range b.st.Todos {
		if s, ok := b.awaiting[t.ID]; ok && s.UpdatedAt.Equal(t.UpdatedAt) && t.Refused == "" {
			if t.DeletedAt != nil {
				continue
			}
			t.Dirty, t.Base, t.NextOnSync = nil, nil, false
		}
		kept = append(kept, t)
	}
	b.st.Todos = kept
	b.awaiting = map[string]Todo{}
}

// sameName reports whether two usernames are the same, null included.
func sameName(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// bindList gives a list made here the id the service gave it. sent is the
// name it was created with: one renamed while that was on its way keeps
// the new name, which goes up as a rename, and one deleted meanwhile is
// deleted there too. The caller holds the lock.
func (b *Backend) bindList(localID, sent string, made api.List) {
	found := false
	for i := range b.st.Lists {
		if l := &b.st.Lists[i]; l.ID == localID {
			found = true
			name := l.Name
			*l = List{List: made}
			if name != sent {
				l.Name, l.Renamed, l.Was = name, true, made.Name
			}
		}
	}
	if !found {
		b.st.Deleted = append(b.st.Deleted, List{List: made})
	}
	for i := range b.st.Todos {
		if t := &b.st.Todos[i]; t.ListID == localID {
			t.Base = t.based()
			t.ListID = made.ID
			if !t.New {
				t.touch(t.UpdatedAt, fList)
			}
		}
	}
}

// hasPending reports whether another push is needed.
func (b *Backend) hasPending() bool {
	for i := range b.st.Todos {
		if b.sendable(&b.st.Todos[i]) {
			return true
		}
	}
	return false
}

// sendable reports whether a todo is waiting to be pushed and can go. One
// in a list the service has not accepted cannot, so it does not count.
// The caller holds the lock.
func (b *Backend) sendable(t *Todo) bool {
	if !t.pending() || t.New && t.ImportedID != "" {
		return false
	}
	if s, ok := b.awaiting[t.ID]; ok && s.UpdatedAt.Equal(t.UpdatedAt) {
		return false // sent already; the pull has yet to show what became of it
	}
	if b.pushes[t.ID] >= maxPushes {
		return false
	}
	l := b.list(t.ListID)
	if l == nil {
		// A todo moved into a list that was then deleted here goes up as a
		// deletion alone, which does not name a list.
		return t.DeletedAt != nil && !t.New && !contains(t.Dirty, fList)
	}
	return !l.Local
}

// changes builds the next batch to push, and keeps each todo in it as it
// was sent, so a later edit can be told apart and the pull compared with
// what went up. The caller holds the lock.
func (b *Backend) changes() ([]map[string]any, map[string]Todo) {
	out := []map[string]any{}
	sent := map[string]Todo{}
	for i := range b.st.Todos {
		if len(out) == pushBatch {
			break
		}
		t := &b.st.Todos[i]
		if !b.sendable(t) {
			continue
		}
		out = append(out, b.changeFor(t))
		s := *t
		s.Steps = slices.Clone(t.Steps)
		sent[t.ID] = s
		if b.pushes != nil {
			b.pushes[t.ID]++
		}
	}
	return out, sent
}

func stamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// changeFor is one todo as a /sync change: only the fields that changed,
// or all of them for a todo the service has never seen.
func (b *Backend) changeFor(t *Todo) map[string]any {
	ch := map[string]any{"id": t.ID, "updatedAt": stamp(&t.UpdatedAt)}
	// A deleted list can physically remove its todos. An offline edit to
	// one must be refused, not read as a new capture in the personal list.
	if !t.New {
		ch["mustExist"] = true
	}
	has := func(f string) bool { return t.New || contains(t.Dirty, f) }
	// For a new todo an empty field is left out rather than sent as null,
	// so the service applies its defaults, such as the reminder.
	set := func(f, key string, empty bool, v any) {
		if has(f) && (!t.New || !empty) {
			ch[key] = v
		}
	}
	if t.DeletedAt != nil {
		ch["deleted"] = true
	}
	set(fTitle, "title", false, t.Title)
	set(fNotes, "notes", t.Notes == nil, t.Notes)
	set(fList, "listId", false, t.ListID)
	set(fAssignee, "assigneeId", t.AssigneeID == nil, t.AssigneeID)
	// The service keeps one do date: an instant for a todo with a time,
	// and a day for one without.
	switch {
	case !has(fDo):
	case t.DoAt == nil:
		set(fDo, "scheduledDay", true, nil)
	case t.HasTime:
		ch["doAt"] = stamp(t.DoAt)
	default:
		ch["scheduledDay"] = b.dayOf(t.DoAt)
	}
	set(fDue, "dueAt", t.DueAt == nil, stamp(t.DueAt))
	set(fRemind, "remindAt", t.RemindAt == nil && t.ImportedID == "", stamp(t.RemindAt))
	set(fRule, "recurrenceRule", t.RecurrenceRule == nil, t.RecurrenceRule)
	if has(fCompleted) && (!t.New || t.IsCompleted) && !t.completesLater() {
		ch["isCompleted"] = t.IsCompleted
		ch["completedAt"] = stamp(t.CompletedAt)
	}
	set(fSort, "sortOrder", t.SortOrder == 0, t.SortOrder)
	if has(fChecklist) && (!t.New || len(t.Steps) > 0) {
		steps := make([]map[string]any, 0, len(t.Steps))
		for _, s := range t.Steps {
			steps = append(steps, map[string]any{"id": s.ID, "title": s.Title, "completedAt": stamp(s.CompletedAt)})
		}
		ch["checklist"] = steps
	}
	return ch
}

// settle records what became of a push: a todo the service made is no
// longer new, an edit it took waits for the pull to show whether it was
// applied, and a todo it refused is marked so it is not sent again as it
// stands. One the service left unapplied is neither: it stays as it was,
// and does not count as a push, so it goes again in the next round without
// using up its maxPushes. It returns the ids of the refused ones. The
// caller holds the lock.
func (b *Backend) settle(sent map[string]Todo, errs []api.SyncError, deferred map[string]bool) []string {
	refused := map[string]string{}
	for _, e := range errs {
		refused[e.ID] = e.Error
	}
	if b.awaiting == nil {
		b.awaiting = map[string]Todo{}
	}
	var ids []string
	kept := b.st.Todos[:0]
	for _, t := range b.st.Todos {
		s, ok := sent[t.ID]
		// A completion held back from the create is still to be sent.
		later := t.completesLater()
		switch {
		case !ok:
		case deferred[t.ID]:
			if b.pushes != nil && b.pushes[t.ID] > 0 {
				b.pushes[t.ID]--
			}
		case refused[t.ID] != "":
			t.Refused = refused[t.ID]
			ids = append(ids, t.ID)
		case !s.New:
			// The service answers the same whether it applied an edit or
			// skipped it for a newer version of its own, which the pull then
			// carries. So the edit stays pending until that shows which.
			b.awaiting[t.ID] = s
		case !t.UpdatedAt.Equal(s.UpdatedAt):
			// Edited while the push was in the air: the service has the
			// todo now, and the newer edit still has to go.
			t.New = false
			if later && !contains(t.Dirty, fCompleted) {
				t.Dirty = append(t.Dirty, fCompleted)
			}
		case t.DeletedAt != nil:
			continue // the deletion is known there; nothing left to keep
		case later:
			t.New, t.Dirty = false, []string{fCompleted}
		default:
			t.New, t.Dirty, t.NextOnSync = false, nil, false
		}
		kept = append(kept, t)
	}
	b.st.Todos = kept
	return ids
}

// stillRefused is the titles of the todos with these ids that stand
// refused, each once. The caller holds the lock.
func (b *Backend) stillRefused(ids []string) []string {
	asked := map[string]bool{}
	for _, id := range ids {
		asked[id] = true
	}
	var titles []string
	for i := range b.st.Todos {
		if t := &b.st.Todos[i]; asked[t.ID] && t.Refused != "" {
			titles = append(titles, t.Title)
		}
	}
	return titles
}

// apply folds a pull into what is kept here. It reports whether anything
// changed, which changes made here gave way to the service's, and what
// became of the unsent todos in lists that left the account. The caller
// holds the lock.
func (b *Backend) apply(ans syncAnswer, seen map[string]bool) (bool, []Replacement, []GoneList) {
	changed := false
	var replaced []Replacement
	var left []GoneList

	// The list snapshot is the truth: a list missing from it is one this
	// account left or that was deleted, and its todos go with it, those with
	// changes not yet sent being dealt with by settleGone. Lists
	// the service has not been told about yet are left alone, and so are
	// changes to lists it has not heard: a list deleted here stays deleted,
	// with its todos, and a name given here stays.
	deleting := map[string]bool{}
	for _, l := range b.st.Deleted {
		deleting[l.ID] = true
	}
	if len(ans.Lists) > 0 {
		lists := make([]List, 0, len(ans.Lists))
		known := map[string]bool{}
		// The lists kept here that the snapshot no longer has, by name.
		names := map[string]string{}
		for _, l := range ans.Lists {
			if deleting[l.ID] {
				continue
			}
			in := List{List: l.List, Members: l.Members}
			if cur := b.list(l.ID); cur != nil && cur.Renamed {
				in.Name, in.Renamed, in.Was = cur.Name, true, l.Name
			}
			lists = append(lists, in)
			known[l.ID] = true
		}
		for _, l := range b.st.Lists {
			if l.Local {
				lists = append(lists, l)
				known[l.ID] = true
			} else if !known[l.ID] {
				changed = true
				names[l.ID] = l.Name
			}
		}
		if len(lists) != len(b.st.Lists) {
			changed = true
		}
		for i := range lists {
			if i < len(b.st.Lists) {
				was := b.st.Lists[i]
				if lists[i].ID != was.ID || lists[i].Name != was.Name || lists[i].ReadOnly != was.ReadOnly || lists[i].Role != was.Role || !reflect.DeepEqual(lists[i].Members, was.Members) {
					changed = true
				}
			}
		}
		b.st.Lists = lists
		left = b.settleGone(known, deleting, names)
		if b.unassignDeparted() {
			changed = true
		}
	}

	at := map[string]int{}
	for i, t := range b.st.Todos {
		at[t.ID] = i
	}
	// Deletions are applied together after the page, so a page of them
	// compacts the todos once instead of once for each.
	gone := map[string]bool{}
	for _, r := range ans.Todos {
		seen[r.ID] = true
		i, have := at[r.ID]
		sent, pushed := b.awaiting[r.ID]
		delete(b.awaiting, r.ID)
		if r.DeletedAt != nil || deleting[r.ListID] && (!have || !b.st.Todos[i].pending()) {
			if !have {
				continue
			}
			gone[r.ID] = true
			// A deletion wins over any change made here: the service keeps
			// a deleted todo deleted whatever edit reaches it later.
			if cur := &b.st.Todos[i]; r.DeletedAt != nil && cur.pending() && cur.DeletedAt == nil {
				replaced = append(replaced, Replacement{ID: cur.ID, Title: cur.Title, Deleted: true})
			}
			continue
		}
		in := Todo{Todo: r.Todo, Steps: r.Checklist}
		if have {
			in.ImportedID = b.st.Todos[i].ImportedID
		}
		for j := range in.Steps {
			in.Steps[j].TodoID = in.ID
		}
		changed = true
		if !have {
			at[r.ID] = len(b.st.Todos)
			b.st.Todos = append(b.st.Todos, in)
			continue
		}
		if cur := b.st.Todos[i]; cur.pending() {
			var s *Todo
			if pushed {
				s = &sent
			}
			var lost []string
			in, lost = merge(cur, in, s)
			if len(lost) > 0 {
				replaced = append(replaced, Replacement{ID: cur.ID, Title: cur.Title, Fields: lost})
			}
		}
		b.st.Todos[i] = in
	}
	if len(gone) > 0 {
		b.dropAll(gone)
		changed = true
	}
	return changed, replaced, left
}

// settleGone settles the todos kept here in lists that are not in the
// account's snapshot, which is what is left of a list that was deleted
// elsewhere or that this account was removed from. known are the lists that
// are in it, deleting the ones deleted here, and names the names of those
// that just left. It returns what became of the todos with unsent changes.
// The caller holds the lock.
//
// A todo with nothing unsent goes without a word, as does one deleted here
// and any in a list deleted here, whose todos go with it. Of the rest:
//   - one made here, which the service has never had, moves to the personal
//     list and goes up there;
//   - one the service has, with a move into the list that was never sent, is
//     put back in the list the service has it in, and keeps its other
//     changes;
//   - any other goes with the list, and its unsent changes are lost.
func (b *Backend) settleGone(known, deleting map[string]bool, names map[string]string) []GoneList {
	var personal *List
	for i := range b.st.Lists {
		if b.st.Lists[i].IsPersonal {
			personal = &b.st.Lists[i]
		}
	}
	var out []GoneList
	report := func(id string) *GoneList {
		name := cmp.Or(names[id], id)
		for i := range out {
			if out[i].Name == name {
				return &out[i]
			}
		}
		out = append(out, GoneList{Name: name})
		return &out[len(out)-1]
	}

	kept := b.st.Todos[:0]
	for _, t := range b.st.Todos {
		switch {
		case known[t.ListID] || deleting[t.ListID] && t.DeletedAt != nil && t.pending():
			kept = append(kept, t)
		case deleting[t.ListID] || t.DeletedAt != nil || !t.New && len(t.Dirty) == 0:
		case t.New && personal != nil:
			g := report(t.ListID)
			g.Moved, g.Into = append(g.Moved, t.Title), personal.Name
			t.ListID = personal.ID
			fields := []string{fList}
			// Nobody else is in the personal list, so the todo cannot stay
			// assigned to someone who is not the person themselves.
			if t.AssigneeID != nil && *t.AssigneeID != b.st.User.ID {
				t.AssigneeID = nil
				fields = append(fields, fAssignee)
			}
			// Any refusal was about the list it was in.
			t.touch(b.now(), fields...)
			kept = append(kept, t)
		case t.New:
			g := report(t.ListID)
			g.Lost = append(g.Lost, t.Title)
		case t.Base != nil && contains(t.Dirty, fList) && known[t.Base.ListID]:
			g := report(t.ListID)
			g.Edits = append(g.Edits, t.Title)
			t.ListID = t.Base.ListID
			t.Dirty = slices.DeleteFunc(slices.Clone(t.Dirty), func(f string) bool { return f == fList })
			if len(t.Dirty) == 0 {
				// Nothing else to send: it is the service's version again.
				t.UpdatedAt, t.Base = t.Base.UpdatedAt, nil
			}
			t.Refused = ""
			kept = append(kept, t)
		default:
			g := report(t.ListID)
			g.Edits = append(g.Edits, t.Title)
		}
	}
	b.st.Todos = kept
	return out
}

// merge folds the service's version of a todo, in, into the one kept here,
// which has changes the service has not confirmed. It returns the result,
// and the names of the fields whose change made here gave way.
//
// A field changed here and not on the service keeps the change, which goes
// up again; one the service holds as it is here needs sending no more; and
// one changed on both sides goes to the change made last, by updatedAt. A
// deletion made here keeps everything, as one made on the service does.
// Without a Base, as for a todo made here or kept by an older evado, what
// the service changed cannot be told, and the change made here is kept.
//
// sent is the todo as this sync pushed it, when in answers that push. A
// field the service holds as sent took the change made here, which was
// changed again since.
func merge(cur, in Todo, sent *Todo) (Todo, []string) {
	base := &Todo{Todo: in.Todo, Steps: slices.Clone(in.Steps)}
	var dirty, lost []string
	for _, f := range cur.Dirty {
		switch {
		case same(f, &in, &cur):
		case cur.DeletedAt != nil, cur.Base == nil, same(f, &in, cur.Base), sent != nil && same(f, &in, sent),
			cur.UpdatedAt.Truncate(time.Millisecond).After(in.UpdatedAt.Truncate(time.Millisecond)):
			carry(f, &in, &cur)
			dirty = append(dirty, f)
		default:
			lost = append(lost, fieldNames[f])
		}
	}
	in.Spawned, in.NextOnSync = cur.Spawned, cur.NextOnSync
	if len(dirty) == 0 {
		in.NextOnSync = false
		return in, lost
	}
	// The service skips a change stamped no later than what it holds, so
	// what is kept goes up stamped after this version, even when it was
	// made before it.
	in.Dirty, in.Base = dirty, base
	in.UpdatedAt = after(in.UpdatedAt, cur.UpdatedAt)
	return in, lost
}

// same reports whether two versions of a todo agree on a field, to the
// millisecond the service keeps times in.
func same(f string, a, b *Todo) bool {
	switch f {
	case fTitle:
		return a.Title == b.Title
	case fNotes:
		return sameText(a.Notes, b.Notes)
	case fList:
		return a.ListID == b.ListID
	case fAssignee:
		return sameText(a.AssigneeID, b.AssigneeID)
	case fDo:
		return sameMilli(a.DoAt, b.DoAt) && (a.DoAt == nil || a.HasTime == b.HasTime)
	case fDue:
		return sameMilli(a.DueAt, b.DueAt)
	case fRemind:
		return sameMilli(a.RemindAt, b.RemindAt)
	case fRule:
		return sameText(a.RecurrenceRule, b.RecurrenceRule)
	case fCompleted:
		return a.IsCompleted == b.IsCompleted
	case fSort:
		return a.SortOrder == b.SortOrder
	case fDeleted:
		return (a.DeletedAt == nil) == (b.DeletedAt == nil)
	case fChecklist:
		return slices.EqualFunc(a.Steps, b.Steps, func(x, y api.ChecklistItem) bool {
			return x.ID == y.ID && x.Title == y.Title && sameMilli(x.CompletedAt, y.CompletedAt)
		})
	}
	return false
}

// sameText compares optional text, where none and empty mean the same.
func sameText(a, b *string) bool {
	text := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return text(a) == text(b)
}

func sameMilli(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Truncate(time.Millisecond).Equal(b.Truncate(time.Millisecond))
}

// unassignDeparted sends to Anyone each todo assigned here, and not yet
// sent, to someone who is no longer in its list. The service has already
// done that to the todos it holds, and it refuses a change that assigns
// someone outside the list, which would hold back the rest of the change.
// It reports whether any todo changed. The caller holds the lock.
func (b *Backend) unassignDeparted() bool {
	changed := false
	for i := range b.st.Todos {
		t := &b.st.Todos[i]
		if t.AssigneeID == nil || !t.New && !contains(t.Dirty, fAssignee) {
			continue
		}
		l := b.list(t.ListID)
		if l == nil || len(l.Members) == 0 {
			continue // who is in the list is not known yet
		}
		member := false
		for _, m := range l.Members {
			member = member || m.UserID == *t.AssigneeID
		}
		if !member {
			t.AssigneeID, t.Refused = nil, ""
			changed = true
		}
	}
	return changed
}

// applyInvitations replaces the kept invitations with a snapshot, empty
// ones included, and leaves them alone when the answer carried none. It
// reports whether they changed. The caller holds the lock.
func (b *Backend) applyInvitations(snapshot []api.Invitation) bool {
	if snapshot == nil {
		return false
	}
	changed := !reflect.DeepEqual(b.st.Invitations, snapshot)
	b.st.Invitations = snapshot
	return changed
}

// keepPending carries the fields still waiting to be pushed from the todo
// kept here onto the one the service sent, stamped after it so that the
// service takes them when they go up.
func keepPending(in, cur Todo) Todo {
	for _, f := range cur.Dirty {
		carry(f, &in, &cur)
	}
	in.Dirty, in.Base = cur.Dirty, cur.Base
	in.ImportedID, in.ImportStarted, in.ImportBase = cur.ImportedID, cur.ImportStarted, cur.ImportBase
	in.UpdatedAt = after(in.UpdatedAt, cur.UpdatedAt)
	in.Spawned, in.NextOnSync = cur.Spawned, cur.NextOnSync
	return in
}

// carry copies one field from the todo kept here onto the service's.
func carry(f string, in, cur *Todo) {
	switch f {
	case fTitle:
		in.Title = cur.Title
	case fNotes:
		in.Notes = cur.Notes
	case fList:
		in.ListID = cur.ListID
	case fAssignee:
		in.AssigneeID = cur.AssigneeID
	case fDo:
		in.DoAt, in.HasTime, in.ScheduledDay = cur.DoAt, cur.HasTime, cur.ScheduledDay
	case fDue:
		in.DueAt = cur.DueAt
	case fRemind:
		// A new time re-armed the reminder here, so a delivery the
		// service reports is for the time that was replaced.
		in.RemindAt, in.RemindedAt = cur.RemindAt, cur.RemindedAt
	case fRule:
		in.RecurrenceRule = cur.RecurrenceRule
	case fCompleted:
		in.IsCompleted, in.CompletedAt = cur.IsCompleted, cur.CompletedAt
	case fSort:
		in.SortOrder = cur.SortOrder
	case fDeleted:
		in.DeletedAt = cur.DeletedAt
	case fChecklist:
		in.Steps = cur.Steps
	}
}
