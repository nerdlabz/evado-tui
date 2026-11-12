package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// service is a stand-in for Evado's sync surface: /me, /lists and /sync
// with last-write-wins on updatedAt, which is all the backend talks to.
type service struct {
	t     *testing.T
	user  api.User
	lists []api.List
	todos map[string]*wireTodo
	// clock is the service's own time, ahead of anything a client sends.
	clock time.Time
	// refuse makes the service turn down a change to this todo id.
	refuse string
	// down makes every request fail as if the network were gone.
	down  atomic.Bool
	syncs int
	// moreSyncs makes /sync say there are more pages for that many calls,
	// counted from the first ever, and failSync makes the call with that
	// number fail with a server error.
	moreSyncs, failSync int
	// listStatus makes creating a list fail with that status, and
	// listRetry is the Retry-After header that comes with it.
	listStatus int
	listRetry  string
	// calls is every request that changed lists or todos, as method and
	// path, in the order they came.
	calls []string
	// got is every change received, in order.
	got []map[string]json.RawMessage
	// members is who every list holds, for a test that cares; by default
	// the account is alone in its lists.
	members []api.Member
	// meBodies is each PATCH /me body received, and inviteBodies each
	// invitation.
	meBodies, inviteBodies []string
	// invites are the account's pending invitations, declined the ones
	// turned down, and noSnapshot leaves the snapshot out of /sync.
	invites, declined []api.Invitation
	noSnapshot        bool
	// noLists makes /sync answer an empty list snapshot, as a broken
	// service would.
	noLists bool
	// inviteReads counts GET /invitations.
	inviteReads int
	// claims counts the claims and releases that were granted.
	claims int
	// successor is the next occurrence each completed repeating todo made,
	// by the id of the one completed. Like the service's recurs_from, it
	// allows one per todo.
	successor map[string]string
	// during runs while a /sync is answered, after its changes are applied
	// and before the pull is read, as someone acting while a push is in the
	// air would.
	during func()
	// capacity, when set, is how many of a /sync's changes the service
	// applies, given the number of the call and how many it holds; a
	// negative answer applies them all. The rest are deferred, reported the
	// way deferAs says. See deferred_test.go.
	capacity func(call, n int) int
	deferAs  string
	// stray is added to the errors of every /sync, as a service that
	// misreports would.
	stray []api.SyncError
	// accepted is the acceptDeferred each /sync carried, and requests the
	// ids it pushed, both in order.
	accepted []bool
	requests [][]string
	// sinces is the cursor each /sync started from, and cursors the one
	// each answered with, both in order.
	sinces, cursors []string
}

func newService(t *testing.T) (*service, string) {
	s := &service{
		t:         t,
		user:      api.User{ID: "u1", Email: "me@example.com", TZ: "UTC"},
		lists:     []api.List{{ID: "p1", Name: "Personal", IsPersonal: true, Role: "owner"}},
		todos:     map[string]*wireTodo{},
		clock:     time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		successor: map[string]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func (s *service) tick() time.Time {
	s.clock = s.clock.Add(time.Second)
	return s.clock
}

func (s *service) serve(w http.ResponseWriter, r *http.Request) {
	if s.down.Load() {
		// Closing the connection without an answer is what a client sees
		// when the network is gone.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return
	}
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case strings.HasPrefix(r.URL.Path, "/invitations"):
		s.serveInvitations(w, r)
	case strings.HasSuffix(r.URL.Path, "/invitations") && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		s.inviteBodies = append(s.inviteBodies, string(raw))
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"ok": true, "invitee": map[string]any{"userId": "u2", "username": "sam"}})
	case strings.HasSuffix(r.URL.Path, "/claim") || strings.HasSuffix(r.URL.Path, "/release"):
		s.serveAssignment(w, r)
	case r.URL.Path == "/people":
		write(map[string]any{"people": []api.Person{{UserID: "u2", Username: strp("sam"), Email: "s***@example.com", SharedListIDs: []string{"p1"}}}})
	case r.URL.Path == "/me" && r.Method == http.MethodPatch:
		raw, _ := io.ReadAll(r.Body)
		s.meBodies = append(s.meBodies, string(raw))
		var body struct {
			SmartParsing *bool           `json:"smartParsing"`
			TZ           *string         `json:"tz"`
			Username     json.RawMessage `json:"username"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.SmartParsing != nil {
			s.user.SmartParsing = *body.SmartParsing
		}
		if body.TZ != nil {
			s.user.TZ = *body.TZ
		}
		if body.Username != nil {
			var name *string
			_ = json.Unmarshal(body.Username, &name)
			if name != nil && *name == "taken" {
				w.WriteHeader(http.StatusConflict)
				write(map[string]any{"error": "That username is taken", "code": "username_taken"})
				return
			}
			s.user.Username = name
		}
		write(map[string]any{"user": s.user})
	case r.URL.Path == "/me":
		write(map[string]any{"user": s.user, "lists": s.lists, "smartParsingAvailable": s.user.SmartParsingAvailable})
	case strings.HasPrefix(r.URL.Path, "/lists/") && (r.Method == http.MethodPatch || r.Method == http.MethodDelete):
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		s.changeList(w, r, strings.TrimPrefix(r.URL.Path, "/lists/"))
	case r.URL.Path == "/lists" && r.Method == http.MethodPost && s.listStatus != 0:
		if s.listRetry != "" {
			w.Header().Set("Retry-After", s.listRetry)
		}
		w.WriteHeader(s.listStatus)
		write(map[string]any{"error": "no list for you", "code": "refused"})
	case r.URL.Path == "/lists" && r.Method == http.MethodPost:
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.calls = append(s.calls, "POST /lists "+body.Name)
		if len(s.lists) >= 25 {
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]any{"error": "too many lists", "code": "invalid_request"})
			return
		}
		l := api.List{ID: "remote-" + strings.ToLower(body.Name), Name: body.Name, Role: "owner"}
		for s.listAt(l.ID) >= 0 {
			l.ID += "-again"
		}
		s.lists = append(s.lists, l)
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"list": l})
	case r.URL.Path == "/sync":
		s.syncs++
		s.calls = append(s.calls, "POST /sync")
		if s.syncs == s.failSync {
			w.WriteHeader(http.StatusInternalServerError)
			write(map[string]any{"error": "boom", "code": "internal"})
			return
		}
		var body struct {
			Since          string                       `json:"since"`
			Changes        []map[string]json.RawMessage `json:"changes"`
			AcceptDeferred bool                         `json:"acceptDeferred"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.accepted = append(s.accepted, body.AcceptDeferred)
		s.sinces = append(s.sinces, body.Since)
		var pushed []string
		for _, ch := range body.Changes {
			var id string
			_ = json.Unmarshal(ch["id"], &id)
			pushed = append(pushed, id)
		}
		s.requests = append(s.requests, pushed)
		// The service checks a whole batch before it reads any of it: a
		// step title over 500 units, or a todo title over 2000, refuses all
		// of it. Only a todo title within that is cut.
		for _, ch := range body.Changes {
			var title string
			var steps []struct{ Title string }
			_ = json.Unmarshal(ch["title"], &title)
			_ = json.Unmarshal(ch["checklist"], &steps)
			tooLong := len(utf16.Encode([]rune(title))) > 2000
			for _, st := range steps {
				tooLong = tooLong || len(utf16.Encode([]rune(st.Title))) > 500
			}
			if tooLong {
				w.WriteHeader(http.StatusBadRequest)
				write(map[string]any{"error": "title too long", "code": "invalid_request"})
				return
			}
		}
		// A cursor is digits to a client; here they are milliseconds.
		var since *time.Time
		if body.Since != "" {
			ms, err := strconv.ParseInt(body.Since, 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				write(map[string]any{"error": "bad cursor", "code": "invalid_request"})
				return
			}
			at := time.UnixMilli(ms)
			since = &at
		}
		errs := []api.SyncError{}
		limit := len(body.Changes)
		if s.capacity != nil {
			if c := s.capacity(s.syncs, limit); c >= 0 && c < limit {
				limit = c
			}
		}
		var deferred []map[string]string
		for i, ch := range body.Changes {
			if i >= limit {
				// Like the service, stop at the first change that does not
				// fit and leave every later one, in order.
				var id string
				_ = json.Unmarshal(ch["id"], &id)
				switch {
				case s.deferAs == "code":
					errs = append(errs, api.SyncError{ID: id, Error: "try again", Code: "deferred"})
				case s.deferAs == "message" || !body.AcceptDeferred:
					errs = append(errs, api.SyncError{ID: id, Error: "not applied: too many changes in one request; send it again"})
				default:
					deferred = append(deferred, map[string]string{"id": id})
				}
				continue
			}
			s.got = append(s.got, ch)
			if msg := s.applyChange(ch); msg != "" {
				var id string
				_ = json.Unmarshal(ch["id"], &id)
				errs = append(errs, api.SyncError{ID: id, Error: msg})
			}
		}
		errs = append(errs, s.stray...)
		if s.during != nil {
			s.during()
		}
		out := []wireTodo{}
		for _, t := range s.todos {
			// The feed is cut to the lists the account is in: a deleted
			// list's rows are gone, and a list this account left is no
			// longer read for it, so neither sends a tombstone.
			if s.listAt(t.ListID) < 0 {
				continue
			}
			switch {
			case since == nil && t.DeletedAt == nil:
				out = append(out, *t)
			case since != nil && t.UpdatedAt.After(*since):
				out = append(out, *t)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
		lists := []wireList{}
		for _, l := range s.lists {
			if s.noLists {
				break
			}
			members := s.members
			if members == nil {
				members = []api.Member{{UserID: "u1", Username: s.user.Username, Email: "me@example.com", Role: "owner"}}
			}
			lists = append(lists, wireList{List: l, Members: members})
		}
		cursor := strconv.FormatInt(s.tick().UnixMilli(), 10)
		s.cursors = append(s.cursors, cursor)
		answer := map[string]any{"todos": out, "lists": lists, "cursor": cursor, "hasMore": s.syncs <= s.moreSyncs, "errors": errs}
		if len(deferred) > 0 {
			answer["deferred"] = deferred
		}
		if !s.noSnapshot {
			// Like the service: a full snapshot, an empty array when none.
			answer["invitations"] = append([]api.Invitation{}, s.invites...)
		}
		write(answer)
	default:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"error": "not found", "code": "not_found"})
	}
}

// listAt is where a list is among the account's, or -1.
func (s *service) listAt(id string) int {
	for i, l := range s.lists {
		if l.ID == id {
			return i
		}
	}
	return -1
}

// changeList renames or deletes a list by the service's rules: only the
// owner may do either, and the personal list is never deleted. Deleting a
// list deletes every todo in it, for everyone.
func (s *service) changeList(w http.ResponseWriter, r *http.Request, id string) {
	write := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	i := s.listAt(id)
	switch {
	case i < 0:
		write(http.StatusNotFound, map[string]any{"error": "not found", "code": "not_found"})
		return
	case r.Method == http.MethodDelete && s.lists[i].IsPersonal:
		write(http.StatusBadRequest, map[string]any{"error": "the personal list cannot be deleted", "code": "invalid_request"})
		return
	case s.lists[i].Role != "owner" && r.Method == http.MethodDelete:
		write(http.StatusForbidden, map[string]any{"error": "only the owner can delete the list", "code": "forbidden"})
		return
	case s.lists[i].Role != "owner":
		write(http.StatusForbidden, map[string]any{"error": "only the owner can rename the list", "code": "forbidden"})
		return
	}
	if r.Method == http.MethodPatch {
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.lists[i].Name = strings.TrimSpace(body.Name)
		write(http.StatusOK, map[string]any{"list": s.lists[i]})
		return
	}
	s.dropList(id)
	write(http.StatusOK, map[string]any{"ok": true})
}

// dropList deletes a list and every todo in it, as the service does when
// its owner deletes it, or as a test does to have someone else delete it.
func (s *service) dropList(id string) {
	at := s.tick()
	for _, t := range s.todos {
		if t.ListID == id && t.DeletedAt == nil {
			t.DeletedAt, t.UpdatedAt = &at, at
		}
	}
	if i := s.listAt(id); i >= 0 {
		s.lists = append(s.lists[:i], s.lists[i+1:]...)
	}
}

// leaveList takes the account out of a list, as the owner removing it or
// its leaving does: the list and its todos stay for everyone else, and this
// account no longer sees either.
func (s *service) leaveList(id string) {
	if i := s.listAt(id); i >= 0 {
		s.lists = append(s.lists[:i], s.lists[i+1:]...)
	}
}

// applyChange is the service's side of /sync, reduced to what the tests
// look at.
func (s *service) applyChange(ch map[string]json.RawMessage) string {
	var id string
	_ = json.Unmarshal(ch["id"], &id)
	if id == s.refuse {
		return "list is read-only"
	}
	var updated time.Time
	_ = json.Unmarshal(ch["updatedAt"], &updated)
	t, exists := s.todos[id]
	// Read from the Worker's source (origin/main 4988c9e, 2026-10-08) and
	// observed on staging on 2026-10-10: deleting a list removes its todo
	// rows, so a change to one is for an id the service no longer knows.
	// Removing a member leaves the rows, which the member's changes then
	// cannot reach. Keep the deleted row for assertions while treating it
	// as absent here.
	if exists && t.DeletedAt != nil && s.listAt(t.ListID) < 0 {
		exists = false
	}
	// With mustExist the absent id is refused; without it the change would
	// be a create, in the personal list unless it names a list. Either way
	// the answer is a 200 whose errors list carries the refusal.
	var mustExist bool
	_ = json.Unmarshal(ch["mustExist"], &mustExist)
	if mustExist && !exists {
		return "not found"
	}
	// A todo in a list the account is no longer in is refused as not found,
	// mustExist or not, the same word as for an id the service never had.
	if exists && s.listAt(t.ListID) < 0 {
		return "not found"
	}
	// Like the service, which keeps milliseconds, a change less than one
	// later than what is stored is no newer, and is skipped without a word.
	if exists && !updated.Truncate(time.Millisecond).After(t.UpdatedAt) {
		return ""
	}
	var assignee *string
	if raw, ok := ch["assigneeId"]; ok {
		_ = json.Unmarshal(raw, &assignee)
		// The whole change is refused, the rest of it included.
		if assignee != nil && !s.member(*assignee) {
			return "assignee is not a member of that list"
		}
	}
	wasOpen := exists && t.CompletedAt == nil
	if !exists {
		t = &wireTodo{Todo: api.Todo{ID: id, ListID: "p1", CreatedAt: s.clock}}
		s.todos[id] = t
	}
	if raw, ok := ch["listId"]; ok {
		was := t.ListID
		_ = json.Unmarshal(raw, &t.ListID)
		if s.listAt(t.ListID) < 0 {
			// The change is refused whole, and a todo it would have made is
			// not made.
			if exists {
				t.ListID = was
			} else {
				delete(s.todos, id)
			}
			return "unknown list"
		}
	}
	if raw, ok := ch["title"]; ok {
		_ = json.Unmarshal(raw, &t.Title)
		t.Title = serviceTitle(t.Title)
	}
	if raw, ok := ch["notes"]; ok {
		t.Notes = nil
		_ = json.Unmarshal(raw, &t.Notes)
	}
	if _, ok := ch["assigneeId"]; ok {
		t.AssigneeID = assignee
	}
	if raw, ok := ch["recurrenceRule"]; ok {
		t.RecurrenceRule = nil
		_ = json.Unmarshal(raw, &t.RecurrenceRule)
	}
	if raw, ok := ch["dueAt"]; ok {
		// Sync clients send the reminder they want, so a deadline alone
		// leaves it where it was.
		t.DueAt = nil
		_ = json.Unmarshal(raw, &t.DueAt)
	}
	if raw, ok := ch["remindAt"]; ok {
		var at *time.Time
		_ = json.Unmarshal(raw, &at)
		// A new time re-arms the reminder; the same time leaves it sent.
		if !sameInstant(at, t.RemindAt) {
			t.RemindedAt = nil
		}
		t.RemindAt = at
	}
	if raw, ok := ch["scheduledDay"]; ok {
		t.ScheduledDay, t.DoAt, t.HasTime = nil, nil, false
		var day string
		if json.Unmarshal(raw, &day) == nil && day != "" {
			at, _ := time.Parse("2006-01-02T15:04", day+"T09:00")
			t.ScheduledDay, t.DoAt = &day, &at
		}
	}
	if raw, ok := ch["doAt"]; ok {
		var at time.Time
		if json.Unmarshal(raw, &at) == nil {
			day := at.UTC().Format("2006-01-02")
			t.DoAt, t.HasTime, t.ScheduledDay = &at, true, &day
		}
	}
	// The checklist lands before the completion, so a next occurrence
	// copies the checklist as submitted.
	if raw, ok := ch["checklist"]; ok {
		t.Checklist = nil
		_ = json.Unmarshal(raw, &t.Checklist)
		for i := range t.Checklist {
			t.Checklist[i].Title = strings.TrimSpace(t.Checklist[i].Title)
		}
	}
	if raw, ok := ch["isCompleted"]; ok {
		_ = json.Unmarshal(raw, &t.IsCompleted)
		t.CompletedAt = nil
		if t.IsCompleted {
			at := s.clock
			t.CompletedAt = &at
		}
	}
	if raw, ok := ch["deleted"]; ok && string(raw) == "true" {
		at := s.clock
		t.DeletedAt = &at
	}
	// The service stamps the change with its own clock.
	t.UpdatedAt = s.tick()
	// Only a todo it already held going from open to completed makes the
	// next occurrence: one created completed makes none.
	if wasOpen && t.IsCompleted && t.RecurrenceRule != nil && t.DeletedAt == nil && s.successor[id] == "" {
		s.spawn(t)
	}
	return ""
}

// serviceTitle is a title as the service stores it: title.trim().slice(0,
// 500), which counts UTF-16 code units and does not trim again. Cutting in
// the middle of a surrogate pair leaves half a character, which the stand-in
// drops, as what the real store does with it is not known.
func serviceTitle(title string) string {
	units := utf16.Encode([]rune(strings.TrimSpace(title)))
	if len(units) > 500 {
		units = units[:500]
		if last := units[499]; last >= 0xD800 && last < 0xDC00 {
			units = units[:499]
		}
	}
	return string(utf16.Decode(units))
}

// spawn makes the next occurrence of a repeating todo, with a server id and
// a copy of the checklist: the same titles in the same order, new ids, and
// every step open.
func (s *service) spawn(done *wireTodo) {
	id := NewID()
	at := s.clock.AddDate(0, 0, 1)
	day := at.Format("2006-01-02")
	next := &wireTodo{Todo: api.Todo{
		ID: id, ListID: done.ListID, AssigneeID: done.AssigneeID, Title: done.Title, Notes: done.Notes,
		RecurrenceRule: done.RecurrenceRule, DoAt: &at, ScheduledDay: &day, RemindAt: &at,
		CreatedAt: s.clock, UpdatedAt: s.tick(),
	}}
	for i, step := range done.Checklist {
		next.Checklist = append(next.Checklist, api.ChecklistItem{ID: NewID(), TodoID: id, Title: step.Title, Position: (i + 1) * 100, CreatedAt: s.clock})
	}
	s.todos[id] = next
	s.successor[done.ID] = id
}

// member reports whether someone is in the account's lists.
func (s *service) member(userID string) bool {
	if s.members == nil {
		return userID == s.user.ID
	}
	for _, m := range s.members {
		if m.UserID == userID {
			return true
		}
	}
	return false
}

// removeMember takes someone out of the lists, and sends their todos in
// them to Anyone, stamped so the change feed carries them.
func (s *service) removeMember(userID string) {
	kept := []api.Member{}
	for _, m := range s.members {
		if m.UserID != userID {
			kept = append(kept, m)
		}
	}
	s.members = kept
	for _, t := range s.todos {
		if t.AssigneeID != nil && *t.AssigneeID == userID {
			t.AssigneeID = nil
			t.UpdatedAt = s.tick()
		}
	}
}

// fire delivers a todo's reminder, as the service's minute job does. That
// moves the todo in the change feed, which this stand-in orders by time.
func (s *service) fire(id string) {
	at := s.tick()
	s.todos[id].RemindedAt = &at
	s.todos[id].UpdatedAt = at
}

// put stores a todo as if another device had made it.
func (s *service) put(id, title string) {
	s.todos[id] = &wireTodo{Todo: api.Todo{ID: id, ListID: "p1", Title: title, CreatedAt: s.clock, UpdatedAt: s.tick()}}
}

func open(t *testing.T, token, url string) *Backend {
	t.Helper()
	b, err := Open(filepath.Join(t.TempDir(), "todos.json"), token, url)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func titles(t *testing.T, c *api.Client) []string {
	t.Helper()
	todos, err := c.ListTodos(context.Background(), api.TodoFilter{Status: api.StatusAll, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, todo := range todos {
		out = append(out, todo.Title)
	}
	sort.Strings(out)
	return out
}

func add(t *testing.T, c *api.Client, title string) api.Todo {
	t.Helper()
	made, err := c.CreateTodos(context.Background(), api.NewTodo{Title: title})
	if err != nil || len(made) == 0 {
		t.Fatalf("create %q: %v", title, err)
	}
	return made[0]
}

func TestWorksWithNoAccount(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	c := b.Client()

	user, lists, err := c.Me(ctx)
	if err != nil || len(lists) != 1 || !lists[0].IsPersonal {
		t.Fatalf("me = %+v %+v %v, want one personal list", user, lists, err)
	}
	todo := add(t, c, "Pay rent tomorrow at 5pm")
	if todo.Title != "Pay rent" || !todo.HasTime || todo.DoAt == nil || todo.ScheduledDay == nil {
		t.Errorf("created %+v, want the date read out of the title", todo)
	}
	if todo.RemindAt == nil || !todo.RemindAt.Equal(*todo.DoAt) {
		t.Errorf("reminder = %v, want it at the todo's time", todo.RemindAt)
	}
	if made, _ := c.CreateTodos(ctx, api.NewTodo{Title: "one\ntwo\n\nthree"}); len(made) != 3 {
		t.Errorf("quick capture made %d todos, want 3", len(made))
	}

	// Steps, completion and deletion all work from the file.
	step, err := c.AddChecklistItem(ctx, todo.ID, "find the chequebook")
	if err != nil {
		t.Fatal(err)
	}
	if ticked, err := c.ToggleChecklistItem(ctx, step); err != nil || !ticked.Completed() {
		t.Errorf("tick = %+v %v", ticked, err)
	} else if back, err := c.ToggleChecklistItem(ctx, ticked); err != nil || back.Completed() {
		t.Errorf("untick = %+v %v, want it open again", back, err)
	}
	if done, _, err := c.CompleteTodo(ctx, todo.ID); err != nil || !done.IsCompleted || done.CompletedAt == nil {
		t.Errorf("complete = %+v %v", done, err)
	}
	if open, _ := c.ListTodos(ctx, api.TodoFilter{Status: api.StatusOpen}); len(open) != 3 {
		t.Errorf("open = %d, want the three captured", len(open))
	}
	if err := c.DeleteTodo(ctx, todo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetTodo(ctx, todo.ID); !api.IsNotFound(err) {
		t.Errorf("deleted todo: err = %v, want not found", err)
	}

	// Sharing needs an account, and says so without signing anyone out.
	if _, err := c.CreateInvite(ctx, lists[0].ID); api.CodeOf(err) != "forbidden" {
		t.Errorf("invite with no account: %v, want forbidden", err)
	}

	// Everything is still there when the program starts again.
	again, err := Open(b.path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(t, again.Client()); strings.Join(got, ",") != "one,three,two" {
		t.Errorf("after reopening: %v", got)
	}
}

// With no account the todos here have no change log: a todo deleted
// before it was ever synced leaves no tombstone, so a feed read from the
// file would miss it. Reading changes since a cursor is refused the way the
// full read refuses one, and the full read itself still answers.
func TestTheChangeFeedNeedsAnAccount(t *testing.T) {
	ctx := context.Background()
	c := open(t, "", "").Client()
	gone := add(t, c, "Gone before any sync")
	add(t, c, "Kept")
	if err := c.DeleteTodo(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	_, feedErr := c.ChangeFeed(ctx, "0")
	_, syncErr := c.Sync(ctx, api.SyncRequest{Since: "0"})
	for name, err := range map[string]error{"feed": feedErr, "sync": syncErr} {
		var apiErr *api.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden || apiErr.Code != api.CodeForbidden ||
			!strings.Contains(apiErr.Message, "needs an Evado account") {
			t.Errorf("%s since a cursor with no account = %v, want 403 forbidden", name, err)
		}
	}
	full, err := c.Sync(ctx, api.SyncRequest{})
	if err != nil || len(full.Todos) != 1 || full.Todos[0].Title != "Kept" {
		t.Errorf("full read = %+v, %v, want the one live todo", full.Todos, err)
	}
}

func TestRepeatingTodoSpawnsItsNextWithNoAccount(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	c := b.Client()
	todo := add(t, c, "Water the plants every week")
	if todo.RecurrenceRule == nil || *todo.RecurrenceRule != "1:week" {
		t.Fatalf("rule = %v, want 1:week", todo.RecurrenceRule)
	}
	_, next, err := c.CompleteTodo(ctx, todo.ID)
	if err != nil || next == nil {
		t.Fatalf("complete: next = %v err = %v, want the next occurrence", next, err)
	}
	if next.Title != "Water the plants" || next.IsCompleted || next.DoAt == nil || !next.DoAt.After(time.Now()) {
		t.Errorf("next = %+v", next)
	}
}

func TestSignInSendsLocalTodosUp(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := open(t, "", "")
	c := b.Client()
	add(t, c, "Buy milk")
	work, err := c.CreateList(ctx, "Work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTodos(ctx, api.NewTodo{Title: "Write the report", ListID: work.ID}); err != nil {
		t.Fatal(err)
	}

	m, err := b.SignIn(ctx, "evt_x", url, false)
	if err != nil || m.Needed {
		t.Fatalf("sign in = %+v %v, want a silent merge into an empty account", m, err)
	}
	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed != 2 || len(svc.todos) != 2 {
		t.Fatalf("pushed %d, service holds %d, want 2", res.Pushed, len(svc.todos))
	}
	for _, todo := range svc.todos {
		switch todo.Title {
		case "Buy milk":
			if todo.ListID != "p1" {
				t.Errorf("personal todo went to %q, want the account's personal list", todo.ListID)
			}
		case "Write the report":
			if todo.ListID != "remote-work" {
				t.Errorf("list todo went to %q, want the list made in the account", todo.ListID)
			}
		}
	}
	if st := b.Status(); st.Pending != 0 || !st.SignedIn || st.LastSync == nil {
		t.Errorf("status after sync = %+v", st)
	}
	// A second sync has nothing to say.
	before := len(svc.got)
	if _, err := b.Sync(ctx); err != nil || len(svc.got) != before {
		t.Errorf("second sync pushed %d more changes, err %v", len(svc.got)-before, err)
	}
}

// Signing in again as the same account sends up only the todos the
// account has not seen; the rest are its own, already there.
func TestSigningInAgainCountsOnlyTodosTheAccountHasNotSeen(t *testing.T) {
	ctx := context.Background()
	_, url := newService(t)
	b := open(t, "", "")
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	add(t, b.Client(), "Already in the account")
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	m, err := b.SignIn(ctx, "evt_x2", url, false)
	if err != nil || m.Needed || m.Local != 0 {
		t.Fatalf("sign in again = %+v %v, want nothing to send", m, err)
	}

	add(t, b.Client(), "Made since the last sync")
	m, err = b.SignIn(ctx, "evt_x3", url, false)
	if err != nil || m.Local != 1 {
		t.Fatalf("sign in again = %+v %v, want the one todo the account has not seen", m, err)
	}
}

func TestSignInAsksBeforeMergingIntoAnAccountWithTodos(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.put("aaaaaaaa-0000-4000-8000-000000000001", "Already in the account")
	b := open(t, "", "")
	add(t, b.Client(), "Made on this computer")

	m, err := b.SignIn(ctx, "evt_x", url, false)
	if err != nil || !m.Needed || m.Local != 1 || m.Server != 1 {
		t.Fatalf("sign in = %+v %v, want a merge question", m, err)
	}
	if b.Status().SignedIn {
		t.Fatal("nothing should change until the merge is agreed to")
	}
	if _, err := b.SignIn(ctx, "evt_x", url, true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(titles(t, b.Client()), ","); got != "Already in the account,Made on this computer" {
		t.Errorf("after merging: %s", got)
	}
	if len(svc.todos) != 2 {
		t.Errorf("service holds %d todos, want both", len(svc.todos))
	}
}

func TestOfflineEditsGoUpLater(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := open(t, "", "")
	c := b.Client()
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	todo := add(t, c, "Book the flights")
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	svc.down.Store(true)
	if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{Title: api.Val("Book the trains")}); err != nil {
		t.Fatalf("editing offline: %v", err)
	}
	if _, _, err := c.CompleteTodo(ctx, todo.ID); err != nil {
		t.Fatalf("completing offline: %v", err)
	}
	offlineOnly := add(t, c, "Made while offline")
	if _, err := b.Sync(ctx); !Offline(err) {
		t.Fatalf("sync with no network: err = %v, want an offline error", err)
	}
	if st := b.Status(); st.Pending != 2 || st.Err == nil {
		t.Errorf("status offline = %+v, want two pending and the error kept", st)
	}

	svc.down.Store(false)
	svc.got = nil
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	up := svc.todos[todo.ID]
	if up.Title != "Book the trains" || !up.IsCompleted {
		t.Errorf("service has %+v, want the offline edits", up.Todo)
	}
	if svc.todos[offlineOnly.ID] == nil {
		t.Error("the todo made offline never reached the service")
	}
	// Only what changed is sent for a todo the service already has.
	for _, ch := range svc.got {
		var id string
		_ = json.Unmarshal(ch["id"], &id)
		if id != todo.ID {
			continue
		}
		if _, sent := ch["notes"]; sent {
			t.Errorf("an untouched field was sent: %v", ch)
		}
		if _, sent := ch["title"]; !sent {
			t.Errorf("the edited title was not sent: %v", ch)
		}
	}
	if st := b.Status(); st.Pending != 0 || st.Err != nil {
		t.Errorf("status after catching up = %+v", st)
	}
}

func TestPullBringsOtherDevicesChanges(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := open(t, "", "")
	c := b.Client()
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	mine := add(t, c, "Mine")
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	// Another device adds one todo, renames mine and deletes nothing yet.
	svc.put("bbbbbbbb-0000-4000-8000-000000000002", "From the phone")
	svc.todos[mine.ID].Title = "Mine, renamed on the phone"
	svc.todos[mine.ID].UpdatedAt = svc.tick()
	res, err := b.Sync(ctx)
	if err != nil || !res.Changed {
		t.Fatalf("sync = %+v %v, want changes", res, err)
	}
	if got := strings.Join(titles(t, c), ","); got != "From the phone,Mine, renamed on the phone" {
		t.Errorf("after pull: %s", got)
	}

	// Then it deletes one, which arrives as a tombstone.
	at := svc.tick()
	svc.todos[mine.ID].DeletedAt = &at
	svc.todos[mine.ID].UpdatedAt = at
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(titles(t, c), ","); got != "From the phone" {
		t.Errorf("after the tombstone: %s", got)
	}
}

func TestDeleteReachesTheService(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := open(t, "", "")
	c := b.Client()
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	todo := add(t, c, "Short-lived")
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTodo(ctx, todo.ID); err != nil {
		t.Fatal(err)
	}
	if b.Status().Pending != 1 {
		t.Error("a deletion the service has not heard of should be pending")
	}
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.todos[todo.ID].DeletedAt == nil {
		t.Error("the service was never told about the deletion")
	}
	b.mu.Lock()
	left := len(b.st.Todos)
	b.mu.Unlock()
	if left != 0 {
		t.Errorf("%d todos kept after the deletion synced, want none", left)
	}
}

func TestRefusedChangeDoesNotBlockTheRest(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b := open(t, "", "")
	c := b.Client()
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	bad := add(t, c, "Will be refused")
	good := add(t, c, "Will go through")
	svc.refuse = bad.ID
	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 1 || res.Refused[0] != "Will be refused" {
		t.Errorf("refused = %v", res.Refused)
	}
	if svc.todos[good.ID] == nil {
		t.Error("the good change was held up by the refused one")
	}
	if st := b.Status(); st.Refused != 1 || st.Pending != 0 {
		t.Errorf("status = %+v, want one refused and nothing pending", st)
	}
	// It is not sent again as it stands, but an edit gives it another go.
	syncs := svc.syncs
	svc.got = nil
	if _, err := b.Sync(ctx); err != nil || len(svc.got) != 0 || svc.syncs != syncs+1 {
		t.Errorf("a refused change was retried unchanged: %v", svc.got)
	}
	svc.refuse = ""
	if _, _, err := c.UpdateTodo(ctx, bad.ID, api.TodoPatch{Title: api.Val("Fixed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(ctx); err != nil || svc.todos[bad.ID] == nil {
		t.Errorf("the edited todo did not go up: %v", err)
	}
}

// An edit made while a push is in the air must not be lost to the answer
// that push brings back.
func TestEditDuringSyncSurvives(t *testing.T) {
	now := time.Now()
	b := &Backend{now: time.Now}
	b.st = fresh(now)
	sent := map[string]Todo{"x": {New: true, Todo: api.Todo{ID: "x", ListID: b.st.Lists[0].ID, Title: "as pushed", UpdatedAt: now}}}
	later := now.Add(time.Second)
	b.st.Todos = []Todo{{New: true, Dirty: []string{fTitle}, Todo: api.Todo{
		ID: "x", ListID: b.st.Lists[0].ID, Title: "edited during the push", UpdatedAt: later,
	}}}
	b.settle(sent, nil, nil)
	if t0 := b.st.Todos[0]; t0.New || !t0.pending() {
		t.Fatalf("after settle: %+v, want it known to the service but still pending", t0)
	}
	// The pull then brings back the older version the service was sent.
	ans := syncAnswer{Todos: []wireTodo{{Todo: api.Todo{ID: "x", ListID: b.st.Lists[0].ID, Title: "as pushed", UpdatedAt: now}}}}
	b.apply(ans, map[string]bool{})
	if got := b.st.Todos[0]; got.Title != "edited during the push" || !got.pending() {
		t.Errorf("after the pull: %+v, want the newer edit kept and still pending", got)
	}
}

func TestSigningInAsSomeoneElseSetsTheTodosAside(t *testing.T) {
	ctx := context.Background()
	_, url := newService(t)
	b := open(t, "", "")
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	add(t, b.Client(), "Belongs to the first account")

	other, otherURL := newService(t)
	other.user.ID = "u2"
	if _, err := b.SignIn(ctx, "evt_y", otherURL, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(other.todos) != 0 {
		t.Error("one account's todos were sent to another")
	}
	if got := titles(t, b.Client()); len(got) != 0 {
		t.Errorf("the second account sees %v", got)
	}
}

// Setting the first account's todos aside must not go ahead if the copy
// cannot be made: the todos would be gone from this computer.
func TestBackupNamesStayBesideTheStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	for _, owner := range []string{"../../escaped", `..\\escaped`, "a/b", "", strings.Repeat("x", 100)} {
		name, err := setAside(path, owner, State{})
		if err != nil {
			t.Fatalf("owner %q: %v", owner, err)
		}
		if filepath.Dir(name) != dir || !strings.HasPrefix(filepath.Base(name), "todos.json.") {
			t.Errorf("owner %q: backup went to %s, want beside %s", owner, name, path)
		}
	}
	if name, _ := setAside(path, "67780128-a067-4d28", State{}); filepath.Base(name) != "todos.json.67780128-a067-4d28.bak" {
		t.Errorf("a plain id should name the backup as it is, got %s", name)
	}
}

func TestSwitchingAccountsKeepsTheTodosWhenTheBackupFails(t *testing.T) {
	ctx := context.Background()
	_, url := newService(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	b, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
		t.Fatal(err)
	}
	add(t, b.Client(), "Belongs to the first account")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	other, otherURL := newService(t)
	other.user.ID = "u2"
	// A read-only directory would do on Unix, but Windows lets files be
	// made in one, so the failure is injected where the backup is created.
	create := createNew
	createNew = func(string) (*os.File, error) { return nil, os.ErrPermission }
	t.Cleanup(func() { createNew = create })
	_, err = b.SignIn(ctx, "evt_y", otherURL, false)
	if err == nil {
		t.Fatal("signing in as someone else went ahead without a backup")
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil || !bytes.Equal(before, after) {
		t.Errorf("the store changed: %v", rerr)
	}
	if got := titles(t, b.Client()); len(got) != 1 {
		t.Errorf("todos in memory = %v, want the first account's one", got)
	}
	if st := b.Status(); !st.SignedIn || st.Server != url {
		t.Errorf("status = %+v, want still tied to the first account", st)
	}
}

// Each switch keeps its own backup: a later one must not replace an earlier.
func TestSwitchingAccountsNeverReplacesABackup(t *testing.T) {
	ctx := context.Background()
	_, urlA := newService(t)
	svcB, urlB := newService(t)
	svcB.user.ID = "u2"
	path := filepath.Join(t.TempDir(), "todos.json")
	b, err := Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	signIn := func(url string) {
		t.Helper()
		if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
			t.Fatal(err)
		}
	}
	signIn(urlA)
	add(t, b.Client(), "first")
	signIn(urlB) // sets the first account's todos aside
	signIn(urlA) // and then the second account's, which are none
	add(t, b.Client(), "second")
	signIn(urlB) // sets the first account's aside a second time

	backups, err := filepath.Glob(path + ".u1*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Fatalf("backups = %v, want two", backups)
	}
	var kept []string
	for _, f := range backups {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, title := range []string{"first", "second"} {
			if strings.Contains(string(raw), `"title":"`+title+`"`) {
				kept = append(kept, title)
			}
		}
	}
	sort.Strings(kept)
	if strings.Join(kept, ",") != "first,second" {
		t.Errorf("backups hold %v, want one of each", kept)
	}
}

// A first pull that stops before the last page has not seen everything the
// account holds, so a todo cached here is not proof that it is gone there.
func TestInitialSyncDoesNotPruneBeforeTheLastPage(t *testing.T) {
	for name, stop := range map[string]func(s *service){
		"round limit": func(s *service) { s.moreSyncs = 1 << 30 },
		"interrupted": func(s *service) { s.failSync = s.syncs + 2; s.moreSyncs = s.syncs + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			svc, url := newService(t)
			svc.put("kept", "Still in the account")
			b := open(t, "", "")
			if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			// Another device removed it, and this computer is asked to start
			// over: the next pull is a first one again.
			delete(svc.todos, "kept")
			svc.put("other", "Somewhere on a later page")
			b.mu.Lock()
			b.st.Cursor = ""
			b.mu.Unlock()

			stop(svc)
			if _, err := b.Sync(ctx); err == nil {
				t.Fatal("sync reported success without reaching the last page")
			}
			if got := titles(t, b.Client()); !strings.Contains(strings.Join(got, ","), "Still in the account") {
				t.Errorf("todos after a partial pull = %v, want the cached one kept", got)
			}
			if st := b.Status(); st.Err == nil {
				t.Errorf("status = %+v, want the failure visible", st)
			}

			// Once a pull does reach the end, the stale todo goes.
			svc.moreSyncs, svc.failSync = 0, 0
			if _, err := b.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			if got := titles(t, b.Client()); strings.Join(got, ",") != "Somewhere on a later page" {
				t.Errorf("todos after a complete pull = %v", got)
			}
		})
	}
}

// A list that cannot be made in the account holds its todos back. The sync
// has to say so, and go on trying, rather than report success.
func TestListCreationFailureIsReportedAndRetried(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retry      string
		retryable  bool
		retryAfter time.Duration
	}{
		{"rate limited", http.StatusTooManyRequests, "7", true, 7 * time.Second},
		{"server error", http.StatusInternalServerError, "", true, 0},
		{"refused", http.StatusUnprocessableEntity, "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, url := newService(t)
			b := open(t, "", "")
			c := b.Client()
			personal := add(t, c, "Goes up anyway")
			work, err := c.CreateList(ctx, "Work")
			if err != nil {
				t.Fatal(err)
			}
			held, err := c.CreateTodos(ctx, api.NewTodo{Title: "Waits for its list", ListID: work.ID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.SignIn(ctx, "evt_x", url, false); err != nil {
				t.Fatal(err)
			}

			svc.listStatus, svc.listRetry = tc.status, tc.retry
			_, err = b.Sync(ctx)
			var apiErr *api.Error
			if err == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
				t.Fatalf("sync = %v, want the list's %d error", err, tc.status)
			}
			if Offline(err) {
				t.Errorf("a %d from the service was taken for being offline", tc.status)
			}
			if apiErr.Retryable() != tc.retryable || apiErr.RetryAfter != tc.retryAfter {
				t.Errorf("error = %+v, want retryable %v after %v", apiErr, tc.retryable, tc.retryAfter)
			}
			if !tc.retryable && !strings.Contains(err.Error(), "Work") {
				t.Errorf("error %q does not name the list", err)
			}
			if st := b.Status(); st.Err == nil || st.LastSync != nil || st.Pending == 0 {
				t.Errorf("status = %+v, want the failure shown and the todo still pending", st)
			}
			if svc.todos[held[0].ID] != nil {
				t.Fatal("a todo went up before its list existed")
			}
			if !tc.retryable && svc.todos[personal.ID] == nil {
				t.Error("one refused list held up the todos in other lists")
			}

			svc.listStatus, svc.listRetry = 0, ""
			if _, err := b.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			if got := svc.todos[held[0].ID]; got == nil || got.ListID != "remote-work" {
				t.Errorf("after the list could be made the todo is %+v, want it in the new list", got)
			}
			if st := b.Status(); st.Pending != 0 || st.Err != nil {
				t.Errorf("status = %+v, want nothing pending", st)
			}
		})
	}
}

// A step patch is all or nothing: one bad field must not leave the good
// ones applied in memory, to be saved by the next unrelated write.
func TestRejectedStepPatchChangesNothing(t *testing.T) {
	for name, body := range map[string]string{
		"bad completion":      `{"title": "renamed", "completedAt": "not a time"}`,
		"blank title":         `{"title": " ", "completedAt": null}`,
		"title of wrong type": `{"title": 7, "completedAt": null}`,
		"invalid json":        `{"title": "renamed", `,
		"empty body":          ``,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "todos.json")
			b, err := Open(path, "", "")
			if err != nil {
				t.Fatal(err)
			}
			c := b.Client()
			todo := add(t, c, "Has steps")
			step, err := c.AddChecklistItem(ctx, todo.ID, "original")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.ToggleChecklistItem(ctx, step); err != nil {
				t.Fatal(err)
			}
			snapshot := func() Todo {
				b.mu.Lock()
				defer b.mu.Unlock()
				got := b.st.Todos[0]
				got.Steps = append([]api.ChecklistItem(nil), got.Steps...)
				got.Dirty = append([]string(nil), got.Dirty...)
				return got
			}
			before := snapshot()

			req, err := http.NewRequest(http.MethodPatch, address+"/checklist/"+step.ID, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			res, err := b.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.StatusCode)
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Errorf("the rejected patch changed the todo:\nbefore %+v\nafter  %+v", before, after)
			}

			// An unrelated write saves whatever is in memory.
			add(t, c, "Unrelated")
			st, err := load(path, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var saved Todo
			for _, td := range st.Todos {
				if td.ID == todo.ID {
					saved = td
				}
			}
			if len(saved.Steps) != 1 || saved.Steps[0].Title != "original" || saved.Steps[0].CompletedAt == nil {
				t.Errorf("saved steps = %+v, want the step as it was", saved.Steps)
			}
		})
	}
}

// A todo deleted here keeps its id until the deletion reaches the service.
// Creating under that id again must not make a second todo beside it.
func TestCreatingUnderADeletedTodosIDIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, b, c := aheadOfService(t)
	id := NewID()
	if _, err := c.CreateTodos(ctx, api.NewTodo{ID: id, Title: "Renew the passport"}); err != nil {
		t.Fatal(err)
	}
	mustSync(t, b)
	if err := c.DeleteTodo(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := c.CreateTodos(ctx, api.NewTodo{ID: id, Title: "Renew the passport"}); api.CodeOf(err) != api.CodeConflict {
		t.Fatalf("create under a deleted todo's id: %v, want a conflict", err)
	}
	b.mu.Lock()
	var kept []Todo
	for _, td := range b.st.Todos {
		if td.ID == id {
			kept = append(kept, td)
		}
	}
	b.mu.Unlock()
	if len(kept) != 1 || kept[0].DeletedAt == nil {
		t.Fatalf("todos with the id = %+v, want only the deleted one", kept)
	}
	mustSync(t, b)
	if up := svc.todos[id]; up == nil || up.DeletedAt == nil {
		t.Errorf("service has %+v, want the todo deleted", up)
	}
}

// The dates a create gives are read or refused, a completion's included,
// so a typo does not quietly become the time it was sent.
func TestUnreadableCompletionIsRefused(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	c := b.Client()
	done, when := true, "not a time"
	if _, err := c.CreateTodos(ctx, api.NewTodo{Title: "Filed the taxes", IsCompleted: &done, CompletedAt: &when}); api.CodeOf(err) != api.CodeInvalidRequest {
		t.Fatalf("create with an unreadable completedAt: %v, want it refused", err)
	}
	if got := titles(t, c); len(got) != 0 {
		t.Errorf("todos = %v, want none made", got)
	}

	when = "2030-04-15T09:30:00Z"
	made, err := c.CreateTodos(ctx, api.NewTodo{Title: "Filed the taxes", IsCompleted: &done, CompletedAt: &when})
	if err != nil {
		t.Fatal(err)
	}
	if at := made[0].CompletedAt; at == nil || !at.Equal(time.Date(2030, 4, 15, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("completedAt = %v, want the one given", at)
	}
}

func TestSortOrderThatIsNotAWholeNumberIsRefused(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	c := b.Client()
	todo := add(t, c, "Water the plants")
	if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{SortOrder: json.RawMessage("4")}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`"first"`, `1.5`, `true`} {
		if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{Title: api.Val("Renamed"), SortOrder: json.RawMessage(raw)}); api.CodeOf(err) != api.CodeInvalidRequest {
			t.Errorf("sortOrder %s: %v, want it refused", raw, err)
		}
	}
	if got := get(t, c, todo.ID); got.SortOrder != 4 || got.Title != "Water the plants" {
		t.Errorf("todo = %q at %d, want it unchanged", got.Title, got.SortOrder)
	}
}

func TestFilters(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	c := b.Client()
	add(t, c, "Undated")
	add(t, c, "Due today today")
	add(t, c, "Later in 5 days")
	late := add(t, c, "Late")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if _, _, err := c.UpdateTodo(ctx, late.ID, api.TodoPatch{ScheduledDay: api.Val(yesterday)}); err != nil {
		t.Fatal(err)
	}
	count := func(status string) int {
		todos, err := c.ListTodos(ctx, api.TodoFilter{Status: status})
		if err != nil {
			t.Fatal(err)
		}
		return len(todos)
	}
	for status, want := range map[string]int{
		api.StatusOpen: 4, api.StatusToday: 1, api.StatusOverdue: 1,
		api.StatusDue: 2, api.StatusUpcoming: 1, api.StatusCompleted: 0, api.StatusAll: 4,
	} {
		if got := count(status); got != want {
			t.Errorf("status %s: %d todos, want %d", status, got, want)
		}
	}
	if found, _ := c.ListTodos(ctx, api.TodoFilter{Query: "LATE"}); len(found) != 2 {
		t.Errorf("search found %d, want the two with late in the title", len(found))
	}
}

// The edit form sends a date typed without a time as midnight with an offset
// and hasTime:false, and it has to stay a do date, not become 00:00.
func TestDateOnlyDoAtStaysADoDate(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	b.st.User.TZ = "Europe/Madrid"
	c := b.Client()
	todo := add(t, c, "Plan")
	patch := api.TodoPatch{DoAt: api.Val("2026-03-10T00:00:00+01:00"), HasTime: api.BoolVal(false)}
	got, _, err := c.UpdateTodo(ctx, todo.ID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasTime || got.ScheduledDay == nil || *got.ScheduledDay != "2026-03-10" {
		t.Errorf("hasTime = %v, day = %v, want a do date on 2026-03-10", got.HasTime, got.ScheduledDay)
	}
	patch = api.TodoPatch{DoAt: api.Val("2026-03-10T15:00:00+01:00"), HasTime: api.BoolVal(true)}
	got, _, err = c.UpdateTodo(ctx, todo.ID, patch)
	if err != nil || !got.HasTime {
		t.Errorf("a time of day should set hasTime: %+v %v", got, err)
	}
}

// A saved view stores "today" as a word, so what it shows is decided by the
// account's timezone on the day it is opened.
func TestTodayIsWorkedOutInTheAccountsTimezone(t *testing.T) {
	ctx := context.Background()
	// 23:30 on the 10th in UTC, already the 11th in Auckland.
	now := time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC)
	for tz, want := range map[string]string{
		"UTC": "On the 10th", "Pacific/Auckland": "On the 11th", "America/Los_Angeles": "On the 10th",
	} {
		b := open(t, "", "")
		b.st.User.TZ = tz
		b.now = func() time.Time { return now }
		c := b.Client()
		tenth, eleventh := add(t, c, "On the 10th"), add(t, c, "On the 11th")
		for id, day := range map[string]string{tenth.ID: "2026-03-10", eleventh.ID: "2026-03-11"} {
			if _, _, err := c.UpdateTodo(ctx, id, api.TodoPatch{ScheduledDay: api.Val(day)}); err != nil {
				t.Fatal(err)
			}
		}
		todos, err := c.ListTodos(ctx, api.TodoFilter{Status: api.StatusToday})
		if err != nil {
			t.Fatal(err)
		}
		if len(todos) != 1 || todos[0].Title != want {
			t.Errorf("in %s today holds %d todos, want only %q", tz, len(todos), want)
		}
	}
}
