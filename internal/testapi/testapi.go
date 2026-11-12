package testapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// The listing limits of the real service: a page of a filtered listing holds
// at most MaxLimit todos (DefaultLimit when no limit is given), and asking
// for more is refused. When more match, the answer carries a nextCursor to
// ask for the next page with.
const (
	DefaultLimit = 100
	MaxLimit     = 200
)

// API is a small in-memory Evado, enough to exercise every operation. It
// exists so the ops and CLI tests can share one stand-in server.
type API struct {
	Todos []api.Todo
	Steps map[string][]api.ChecklistItem
	Lists []api.List
	// Invitations are the pending invitations the change feed snapshots.
	Invitations []api.Invitation
	Members     []api.Member
	Parsed      api.ParsePreview
	Patches     []map[string]json.RawMessage
	nextID      int
	nextStep    int
	// FailWith, when set, is returned for every todo request.
	FailWith *api.Error
	// Creates counts create requests, and CreateError, when set, can fail
	// the nth (from 1), so a test can break a batch part way through.
	Creates     int
	CreateError func(n int) *api.Error
	// FeedPageSize is how many todos one change-feed page holds. It is
	// MaxLimit unless a test wants smaller pages.
	FeedPageSize int
	// FeedRequests counts change-feed pulls, so a test can tell how many
	// listings a command made.
	FeedRequests int
	// FeedSince records the cursor of every pull, in order, empty for an
	// initial sync, so a test can check that a client followed the server's
	// cursor.
	FeedSince []string
	// ListQueries records the query string of every filtered list request,
	// in order, so a test can check which pages a client asked for.
	ListQueries []string
	// OnFeed, when set, runs before pull n (counting from 1) is answered, so
	// a test can change lists or invitations between pages.
	OnFeed func(n int)
	// FeedError, when set, may fail pull n (counting from 1).
	FeedError func(n int) *api.Error
	// OmitInvitations leaves the invitations snapshot out of the feed, as a
	// server that does not send one would.
	OmitInvitations bool
	// MeStatus, when set, makes /me fail with that status.
	MeStatus int
	// TZ is the account timezone /me reports. It is Europe/Madrid when empty.
	TZ string
	// ForeignLists are the ids of lists other accounts own, and DeletedLists
	// those of lists deleted, which POST /lists refuses to make as the
	// service does. ListCreates records the id each one asked for, empty
	// for none.
	ForeignLists, DeletedLists []string
	ListCreates                []string
	nextList                   int
}

// feedEpoch orders the change feed: a todo without an UpdatedAt changed one
// second after the one before it in Todos.
var feedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// changedAt is when a todo last changed, for the change feed.
func (f *API) changedAt(i int) time.Time {
	if u := f.Todos[i].UpdatedAt; !u.IsZero() {
		return u
	}
	return feedEpoch.Add(time.Duration(i+1) * time.Second)
}

// Cursor is the cursor this fake gives for a change made at t. Like the
// service's it is digits and means nothing to a client; here it is the
// milliseconds since the epoch, which is what lets tests name one.
func Cursor(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

// pageCursorShape is what the service accepts as the cursor of a page of the
// list: an effective date, a creation time and an id, joined by colons.
var pageCursorShape = regexp.MustCompile(`^(-?\d*):(-?\d+):([\w-]+)$`)

// PageCursor is the cursor this fake gives after the page that ends with t,
// in the shape the service uses. The fake lists in the order of Todos, so
// only the id says where to resume; the rest is there so that a client
// cannot tell it apart from the service's.
func PageCursor(t api.Todo) string {
	eff := ""
	switch {
	case t.DoAt != nil:
		eff = strconv.FormatInt(t.DoAt.UnixMilli(), 10)
	case t.DueAt != nil:
		eff = strconv.FormatInt(t.DueAt.UnixMilli(), 10)
	}
	return eff + ":" + strconv.FormatInt(t.CreatedAt.UnixMilli(), 10) + ":" + t.ID
}

// cursorID is the todo a page cursor stops after. A cursor the service would
// refuse as malformed gives false.
func cursorID(raw string) (string, bool) {
	m := pageCursorShape.FindStringSubmatch(raw)
	if m == nil || len(raw) > 120 {
		return "", false
	}
	return m[3], true
}

// feed answers GET /todos?since= and POST /sync, the pulls that page. An
// empty cursor is an initial sync.
func (f *API) feed(cursor string) (int, map[string]any) {
	var since time.Time
	if cursor != "" {
		ms, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return http.StatusBadRequest, map[string]any{"error": "bad cursor", "code": api.CodeInvalidRequest}
		}
		since = time.UnixMilli(ms)
	} else {
		since = time.UnixMilli(0)
	}
	f.FeedRequests++
	f.FeedSince = append(f.FeedSince, cursor)
	if f.OnFeed != nil {
		f.OnFeed(f.FeedRequests)
	}
	if f.FeedError != nil {
		if e := f.FeedError(f.FeedRequests); e != nil {
			return e.StatusCode, map[string]any{"error": e.Code, "code": e.Code}
		}
	}
	size := f.FeedPageSize
	if size <= 0 {
		size = MaxLimit
	}
	var page []api.SyncTodo
	more := false
	last := since
	for i := range f.Todos {
		at := f.changedAt(i)
		if !at.After(since) {
			continue
		}
		if len(page) == size {
			more = true
			break
		}
		steps := f.Steps[f.Todos[i].ID]
		if steps == nil {
			steps = []api.ChecklistItem{}
		}
		page = append(page, api.SyncTodo{Todo: f.Todos[i], Checklist: steps})
		last = at
	}
	if page == nil {
		page = []api.SyncTodo{}
	}
	body := map[string]any{"todos": page, "lists": f.Lists, "cursor": Cursor(last), "hasMore": more}
	if !f.OmitInvitations {
		inv := f.Invitations
		if inv == nil {
			inv = []api.Invitation{}
		}
		body["invitations"] = inv
	}
	return http.StatusOK, body
}

// readOnly reports whether a list is one of the read-only shared lists, where
// the service allows completing things and nothing else.
func (f *API) readOnly(listID string) bool {
	for _, l := range f.Lists {
		if l.ID == listID {
			return l.ReadOnly
		}
	}
	return false
}

// New returns a fake with a few todos, two lists and two members.
func New() *API {
	day := "2026-03-10"
	return &API{
		Todos: []api.Todo{
			{ID: "aaa111", ListID: "l1", Title: "Pay the cable bill", ScheduledDay: &day},
			{ID: "aaa222", ListID: "l1", Title: "Renew passport"},
			{ID: "bbb333", ListID: "l2", Title: "Buy milk", IsCompleted: true},
		},
		Steps: map[string][]api.ChecklistItem{},
		Lists: []api.List{
			{ID: "l1", Name: "Personal", IsPersonal: true},
			{ID: "l2", Name: "Work"},
		},
		Members: []api.Member{{UserID: "u1", Email: "me@example.com"}, {UserID: "u2", Email: "sam@example.com"}},
		nextID:  1,
	}
}

// Find returns the todo with an id, or nil.
func (f *API) Find(id string) *api.Todo {
	for i := range f.Todos {
		if f.Todos[i].ID == id {
			return &f.Todos[i]
		}
	}
	return nil
}

// Client starts the fake on a local server and returns a client for it.
func (f *API) Client(t *testing.T) *api.Client {
	t.Helper()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	fail := func(w http.ResponseWriter, status int, code string) {
		w.WriteHeader(status)
		write(w, map[string]any{"error": code, "code": code})
	}
	// The service reads every timestamp as RFC 3339 and refuses one with no
	// offset, naming the field. A bare scheduledDay must be YYYY-MM-DD.
	invalidDatetime := func(w http.ResponseWriter, path string) {
		w.WriteHeader(http.StatusBadRequest)
		write(w, map[string]any{
			"error": "Bad request", "code": api.CodeInvalidRequest,
			"fields": []api.FieldError{{Path: path, Message: "Invalid datetime"}},
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if f.MeStatus != 0 {
			fail(w, f.MeStatus, "server_error")
			return
		}
		tz := f.TZ
		if tz == "" {
			tz = "Europe/Madrid"
		}
		write(w, map[string]any{"user": api.User{ID: "u1", Email: "me@example.com", TZ: tz}, "lists": f.Lists})
	})
	mux.HandleFunc("/lists", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			f.createList(w, r)
			return
		}
		write(w, map[string]any{"lists": f.Lists})
	})
	mux.HandleFunc("/lists/", func(w http.ResponseWriter, r *http.Request) { write(w, map[string]any{"members": f.Members}) })
	mux.HandleFunc("/parse", func(w http.ResponseWriter, r *http.Request) { write(w, f.Parsed) })
	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Since string `json:"since"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		status, body := f.feed(in.Since)
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		write(w, body)
	})
	mux.HandleFunc("/todos", func(w http.ResponseWriter, r *http.Request) {
		if f.FailWith != nil {
			fail(w, f.FailWith.StatusCode, f.FailWith.Code)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var n api.NewTodo
			_ = json.NewDecoder(r.Body).Decode(&n)
			f.Creates++
			if f.CreateError != nil {
				if e := f.CreateError(f.Creates); e != nil {
					w.WriteHeader(e.StatusCode)
					write(w, map[string]any{"error": e.Code, "code": e.Code})
					return
				}
			}
			// The service refuses a title over 2000 UTF-16 units outright,
			// even when its lines would each be a todo of their own.
			if len(utf16.Encode([]rune(n.Title))) > 2000 {
				fail(w, http.StatusBadRequest, api.CodeInvalidRequest)
				return
			}
			id := n.ID
			if id == "" {
				id = fmt.Sprintf("new%03d", f.nextID)
				f.nextID++
			}
			if f.readOnly(n.ListID) {
				fail(w, http.StatusForbidden, api.CodeListReadOnly)
				return
			}
			for _, d := range []struct {
				path string
				val  *string
			}{{"doAt", n.DoAt}, {"dueAt", n.DueAt}, {"remindAt", n.RemindAt}, {"completedAt", n.CompletedAt}} {
				if d.val != nil {
					if _, err := time.Parse(time.RFC3339, *d.val); err != nil {
						invalidDatetime(w, d.path)
						return
					}
				}
			}
			if n.ScheduledDay != nil {
				if _, err := time.Parse("2006-01-02", *n.ScheduledDay); err != nil {
					invalidDatetime(w, "scheduledDay")
					return
				}
			}
			var made []api.Todo
			lines := strings.Split(n.Title, "\n")
			// Like the service, turn at most 20 lines into todos and drop
			// the rest without an error.
			if len(lines) > 20 {
				lines = lines[:20]
			}
			for _, line := range lines {
				todo := api.Todo{ID: id, ListID: n.ListID, Title: storedTitle(line), Notes: n.Notes}
				if todo.ListID == "" {
					todo.ListID = "l1"
				}
				f.Todos = append(f.Todos, todo)
				made = append(made, todo)
				id = fmt.Sprintf("new%03d", f.nextID)
				f.nextID++
			}
			w.WriteHeader(http.StatusCreated)
			write(w, map[string]any{"todos": made})
		default:
			if raw := r.URL.Query().Get("since"); raw != "" {
				status, body := f.feed(raw)
				if status != http.StatusOK {
					w.WriteHeader(status)
				}
				write(w, body)
				return
			}
			f.ListQueries = append(f.ListQueries, r.URL.RawQuery)
			limit := DefaultLimit
			if raw := r.URL.Query().Get("limit"); raw != "" {
				n, err := strconv.Atoi(raw)
				if err != nil || n < 1 || n > MaxLimit {
					fail(w, http.StatusBadRequest, api.CodeInvalidRequest)
					return
				}
				limit = n
			}
			// The cursor is where the previous page stopped: the todo it names
			// is the last one that page held, so this page starts after it.
			start := 0
			if raw, given := r.URL.Query()["cursor"]; given {
				id, ok := cursorID(raw[0])
				if !ok {
					fail(w, http.StatusBadRequest, api.CodeInvalidRequest)
					return
				}
				start = len(f.Todos)
				for i := range f.Todos {
					if f.Todos[i].ID == id {
						start = i + 1
						break
					}
				}
			}
			status, q, list := r.URL.Query().Get("status"), strings.ToLower(r.URL.Query().Get("q")), r.URL.Query().Get("listId")
			out := []api.Todo{}
			var next *string
			for _, todo := range f.Todos[start:] {
				if todo.DeletedAt != nil {
					continue
				}
				if status == "open" && todo.IsCompleted {
					continue
				}
				if status == "completed" && !todo.IsCompleted {
					continue
				}
				if q != "" && !strings.Contains(strings.ToLower(todo.Title), q) {
					continue
				}
				if list != "" && todo.ListID != list {
					continue
				}
				if len(out) == limit {
					c := PageCursor(out[len(out)-1])
					next = &c
					break
				}
				out = append(out, todo)
			}
			write(w, map[string]any{"todos": out, "nextCursor": next})
		}
	})
	mux.HandleFunc("/todos/", func(w http.ResponseWriter, r *http.Request) {
		if f.FailWith != nil {
			fail(w, f.FailWith.StatusCode, f.FailWith.Code)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/todos/"), "/")
		todo := f.Find(parts[0])
		if todo == nil || todo.DeletedAt != nil {
			fail(w, http.StatusNotFound, api.CodeNotFound)
			return
		}
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}
		switch {
		case action == "complete":
			todo.IsCompleted = true
			write(w, map[string]any{"todo": todo, "next": nil})
		case action == "uncomplete":
			todo.IsCompleted = false
			write(w, map[string]any{"todo": todo})
		case action == "claim" || action == "release":
			status, body := f.assignment(todo, action == "claim")
			w.WriteHeader(status)
			write(w, body)
		case action == "checklist" && r.Method == http.MethodPost:
			if f.readOnly(todo.ListID) {
				fail(w, http.StatusForbidden, api.CodeListReadOnly)
				return
			}
			var b struct{ Title string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			if tooLongForStep(b.Title) {
				fail(w, http.StatusBadRequest, api.CodeInvalidRequest)
				return
			}
			f.nextStep++
			item := api.ChecklistItem{ID: fmt.Sprintf("step%03d", f.nextStep), TodoID: todo.ID, Title: strings.TrimSpace(b.Title), Position: len(f.Steps[todo.ID])}
			f.Steps[todo.ID] = append(f.Steps[todo.ID], item)
			w.WriteHeader(http.StatusCreated)
			write(w, map[string]any{"item": item})
		case action == "checklist":
			items := f.Steps[todo.ID]
			if items == nil {
				items = []api.ChecklistItem{}
			}
			write(w, map[string]any{"items": items})
		case r.Method == http.MethodPatch:
			var p map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&p)
			if f.readOnly(todo.ListID) && !onlyCompletion(p) {
				fail(w, http.StatusForbidden, api.CodeListReadOnly)
				return
			}
			stamps := map[string]**time.Time{
				"doAt": &todo.DoAt, "dueAt": &todo.DueAt, "remindAt": &todo.RemindAt, "completedAt": &todo.CompletedAt,
			}
			for _, key := range []string{"doAt", "dueAt", "remindAt", "completedAt"} {
				raw, ok := p[key]
				var s *string
				if !ok || json.Unmarshal(raw, &s) != nil || s == nil {
					continue
				}
				if _, err := time.Parse(time.RFC3339, *s); err != nil {
					invalidDatetime(w, key)
					return
				}
			}
			if raw, ok := p["scheduledDay"]; ok {
				var s *string
				if json.Unmarshal(raw, &s) == nil && s != nil {
					if _, err := time.Parse("2006-01-02", *s); err != nil {
						invalidDatetime(w, "scheduledDay")
						return
					}
				}
			}
			f.Patches = append(f.Patches, p)
			for key, dst := range stamps {
				if raw, ok := p[key]; ok {
					*dst = nil
					_ = json.Unmarshal(raw, dst)
				}
			}
			if raw, ok := p["scheduledDay"]; ok {
				todo.ScheduledDay = nil
				_ = json.Unmarshal(raw, &todo.ScheduledDay)
			}
			if raw, ok := p["hasTime"]; ok {
				_ = json.Unmarshal(raw, &todo.HasTime)
			} else if _, ok := p["doAt"]; ok {
				todo.HasTime = todo.DoAt != nil
			}
			if raw, ok := p["title"]; ok {
				_ = json.Unmarshal(raw, &todo.Title)
				todo.Title = storedTitle(todo.Title)
			}
			if raw, ok := p["isCompleted"]; ok {
				_ = json.Unmarshal(raw, &todo.IsCompleted)
			}
			write(w, map[string]any{"todo": todo, "next": nil})
		case r.Method == http.MethodDelete:
			if f.readOnly(todo.ListID) {
				fail(w, http.StatusForbidden, api.CodeListReadOnly)
				return
			}
			kept := f.Todos[:0]
			for _, x := range f.Todos {
				if x.ID != todo.ID {
					kept = append(kept, x)
				}
			}
			f.Todos = kept
			write(w, map[string]any{"ok": true})
		default:
			write(w, map[string]any{"todo": todo})
		}
	})
	mux.HandleFunc("/checklist/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/checklist/")
		var item *api.ChecklistItem
		var todoID string
		for tid, items := range f.Steps {
			for i := range items {
				if items[i].ID == id {
					item, todoID = &items[i], tid
				}
			}
		}
		if item == nil {
			fail(w, http.StatusNotFound, api.CodeNotFound)
			return
		}
		ro := false
		if todo := f.Find(todoID); todo != nil {
			ro = f.readOnly(todo.ListID)
		}
		switch r.Method {
		case http.MethodPatch:
			var p map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&p)
			if _, renames := p["title"]; ro && renames {
				fail(w, http.StatusForbidden, api.CodeListReadOnly)
				return
			}
			if raw, ok := p["title"]; ok {
				var title string
				_ = json.Unmarshal(raw, &title)
				if tooLongForStep(title) {
					fail(w, http.StatusBadRequest, api.CodeInvalidRequest)
					return
				}
				item.Title = strings.TrimSpace(title)
			}
			if raw, ok := p["completedAt"]; ok {
				item.CompletedAt = nil
				_ = json.Unmarshal(raw, &item.CompletedAt)
			}
			write(w, map[string]any{"item": item})
		case http.MethodDelete:
			if ro {
				fail(w, http.StatusForbidden, api.CodeListReadOnly)
				return
			}
			kept := f.Steps[todoID][:0]
			for _, x := range f.Steps[todoID] {
				if x.ID != id {
					kept = append(kept, x)
				}
			}
			f.Steps[todoID] = kept
			write(w, map[string]any{"ok": true})
		default:
			fail(w, http.StatusMethodNotAllowed, api.CodeBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return api.New("evt_test", api.WithBaseURL(srv.URL))
}

// Me is the account the fake answers as.
const Me = "u1"

// assignment answers a claim or release the way the service does: the
// change is made only from the state it expects, a retry that already took
// effect returns the todo unchanged, and losing names who has the todo,
// except for a done todo, where the service sends neither.
func (f *API) assignment(todo *api.Todo, claim bool) (int, map[string]any) {
	for _, l := range f.Lists {
		if l.ID == todo.ListID && l.IsPersonal {
			return http.StatusBadRequest, map[string]any{"error": "only todos in shared lists can be claimed or returned to Anyone", "code": api.CodePersonalList}
		}
	}
	if f.readOnly(todo.ListID) {
		return http.StatusForbidden, map[string]any{"error": "this list is read-only", "code": api.CodeListReadOnly}
	}
	if todo.IsCompleted {
		return http.StatusConflict, map[string]any{"error": "this todo is already done", "code": api.CodeTodoCompleted}
	}
	var holder *api.Member
	if todo.AssigneeID != nil {
		for i := range f.Members {
			if f.Members[i].UserID == *todo.AssigneeID {
				holder = &f.Members[i]
			}
		}
	}
	mine := todo.AssigneeID != nil && *todo.AssigneeID == Me
	switch {
	case claim && (mine || holder == nil):
		me := Me
		todo.AssigneeID = &me
	case claim:
		return http.StatusConflict, map[string]any{"error": "someone else already claimed this todo", "code": api.CodeAlreadyClaimed, "todo": todo, "assignee": holder}
	case mine || todo.AssigneeID == nil:
		todo.AssigneeID = nil
	default:
		return http.StatusConflict, map[string]any{"error": "this todo isn't assigned to you", "code": api.CodeNotAssignedToYou, "todo": todo, "assignee": holder}
	}
	return http.StatusOK, map[string]any{"todo": todo}
}

// onlyCompletion reports whether a todo patch does nothing but mark it done
// or not done, the one change a read-only list allows.
func onlyCompletion(p map[string]json.RawMessage) bool {
	for k := range p {
		if k != "isCompleted" && k != "completedAt" {
			return false
		}
	}
	return true
}

// storedTitle is a todo title as the service stores it: title.trim().slice(0,
// 500), which counts UTF-16 code units and does not trim again. Cutting in
// the middle of a surrogate pair leaves half a character, which this drops,
// as what the real store does with it is not known.
func storedTitle(title string) string {
	units := utf16.Encode([]rune(strings.TrimSpace(title)))
	if len(units) > 500 {
		units = units[:500]
		if last := units[499]; last >= 0xD800 && last < 0xDC00 {
			units = units[:499]
		}
	}
	return string(utf16.Decode(units))
}

// tooLongForStep is whether the service would refuse a step title outright.
// It does not cut one: only the sync route cuts, to the same 500 units.
func tooLongForStep(title string) bool {
	return len(utf16.Encode([]rune(title))) > 500
}

// createList answers POST /lists as the service does. An id makes the
// create safe to send again: an unused one is the new list's, one of a list
// the account owns answers that list as it is, even under another name, one
// it may not use (another account's list, a list it is only a member of,
// or its personal list) is a conflict, and one of a deleted list is gone
// for good.
func (f *API) createList(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID, Name string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.ListCreates = append(f.ListCreates, body.ID)
	answer := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if body.ID != "" {
		for _, l := range f.Lists {
			if l.ID != body.ID {
				continue
			}
			if l.IsPersonal || l.ReadOnly || l.Role != "" && l.Role != "owner" {
				answer(http.StatusConflict, map[string]any{"error": "that id is in use", "code": api.CodeIDConflict})
				return
			}
			answer(http.StatusOK, map[string]any{"list": l, "idempotent": true})
			return
		}
		switch {
		case slices.Contains(f.ForeignLists, body.ID):
			answer(http.StatusConflict, map[string]any{"error": "that id is in use", "code": api.CodeIDConflict})
			return
		case slices.Contains(f.DeletedLists, body.ID):
			answer(http.StatusGone, map[string]any{"error": "that list was deleted", "code": api.CodeListDeleted})
			return
		}
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		answer(http.StatusBadRequest, map[string]any{"error": "a list needs a name", "code": api.CodeInvalidRequest})
		return
	}
	l := api.List{ID: body.ID, Name: name, Role: "owner"}
	if l.ID == "" {
		f.nextList++
		l.ID = fmt.Sprintf("list%03d", f.nextList)
	}
	f.Lists = append(f.Lists, l)
	answer(http.StatusCreated, map[string]any{"list": l})
}
