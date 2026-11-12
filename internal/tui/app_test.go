package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
)

// patchCall is one recorded PATCH request.
type patchCall struct {
	id   string
	body map[string]json.RawMessage
}

// fakeAPI is a stand-in for the Evado service, just complete enough to
// drive every view.
type fakeAPI struct {
	smartAvailable, smartParsing bool
	creates                      []api.NewTodo
	parseAnswer                  func(string, *bool) api.ParsePreview
	todos                        []api.Todo
	lists                        []api.List
	members                      []api.Member
	steps                        map[string][]api.ChecklistItem
	writes                       int
	// feeds and loads count change-feed pulls and full list loads.
	feeds, loads int
	// stubFeed makes every feed pull carry one fixed change, for the Sync tab.
	stubFeed bool
	// patches records each PATCH: the todo id and its body.
	patches []patchCall
	// created records each create request's body.
	created []api.NewTodo
	// parsed, when set, is what /parse answers whatever the text.
	parsed map[string]any

	deletes []string
	// stepReads records the todo of each GET of a checklist.
	stepReads []string
	// refuse, when set, picks requests to answer with a server error, so a
	// test can make one action fail and the next attempt succeed.
	refuse func(r *http.Request) bool
	// noAccountScope answers full export and account deletion the way the
	// service does for a token without the account scope.
	noAccountScope bool
	// accountCalls counts full export and delete requests that arrived.
	accountCalls     int
	importCalls      []bool
	importBodies     [][]byte
	importIncomplete bool
	portableExports  int

	// userID is who GET /me says the account is, "u1" unless set. tz is
	// the account's timezone, UTC unless a test or PATCH /me sets it.
	userID string
	tz     string
	// clock is the server's idea of now, which decides what "today" is in
	// the account's timezone. Left nil, the filtered list uses the real
	// time and this machine's own day, as it always has.
	clock func() time.Time
	// queries records the filters of each filtered GET /todos, and
	// membersBy the members of a list, where they differ from the default.
	queries   []url.Values
	membersBy map[string][]api.Member

	// username is the account's username, and meBodies each PATCH /me.
	username *string
	meBodies []string
	// meReads counts GET /me.
	meReads int
	// people is what GET /people answers, and invites each invitation
	// request body. inviteErr, when set, refuses invitations with that
	// status and code.
	people    []api.Person
	invites   []map[string]string
	inviteErr *apiFailure
	// peopleErr makes GET /people fail.
	peopleErr *apiFailure

	// pending are the pending invitations and declined the ones turned
	// down. answers records each accept, decline or restore as "action id",
	// answerErr refuses an action, and invitesErr fails the inbox read.
	pending, declined []api.Invitation
	answers           []string
	answerErr         map[string]*apiFailure
	invitesErr        *apiFailure
	inviteReads       int
}

// apiFailure is an error answer a test wants from the fake.
type apiFailure struct {
	status     int
	code, msg  string
	retryAfter string
}

// fail sends an API-shaped error, so error handling is exercised too.
func (f *fakeAPI) fail(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "code": code})
}

func (f *fakeAPI) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, body any) {
		_ = json.NewEncoder(w).Encode(body)
	}
	if f.tz == "" {
		f.tz = "UTC"
	}
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			f.accountCalls++
			if f.noAccountScope {
				f.deny(w)
				return
			}
		}
		user := func() map[string]any {
			id := f.userID
			if id == "" {
				id = "u1"
			}
			return map[string]any{
				"id": id, "email": "demo@example.com", "tz": f.tz, "username": f.username,
				"createdAt": "2026-01-01T00:00:00Z", "smartParsing": f.smartParsing,
			}
		}
		if r.Method == http.MethodPatch {
			raw, _ := io.ReadAll(r.Body)
			f.meBodies = append(f.meBodies, string(raw))
			var body struct {
				SmartParsing *bool           `json:"smartParsing"`
				TZ           *string         `json:"tz"`
				Username     json.RawMessage `json:"username"`
			}
			_ = json.Unmarshal(raw, &body)
			if body.SmartParsing != nil {
				if !f.smartAvailable {
					f.fail(w, http.StatusBadRequest, "smart_parsing_unavailable", "Smarter dates are unavailable")
					return
				}
				f.smartParsing = *body.SmartParsing
			}
			if body.Username != nil {
				var name *string
				_ = json.Unmarshal(body.Username, &name)
				switch {
				case name != nil && *name == "taken":
					f.fail(w, http.StatusConflict, "username_taken", "That username is already taken")
					return
				case name != nil && *name == "no":
					f.fail(w, http.StatusBadRequest, "username_invalid", "Usernames are 3 to 20 characters")
					return
				case name != nil:
					clean := strings.ToLower(strings.TrimPrefix(*name, "@"))
					name = &clean
				}
				f.username = name
			}
			if body.TZ != nil {
				f.tz = *body.TZ
			}
			write(w, map[string]any{"user": user()})
			return
		}
		f.meReads++
		write(w, map[string]any{"user": user(), "lists": f.lists, "smartParsingAvailable": f.smartAvailable})
	})
	mux.HandleFunc("/invitations", func(w http.ResponseWriter, r *http.Request) {
		f.inviteReads++
		if e := f.invitesErr; e != nil {
			f.fail(w, e.status, e.code, e.msg)
			return
		}
		write(w, map[string]any{"invitations": append([]api.Invitation{}, f.pending...)})
	})
	mux.HandleFunc("/invitations/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/invitations/"), "/")
		id, action := parts[0], parts[len(parts)-1]
		f.answers = append(f.answers, action+" "+id)
		if e := f.answerErr[action]; e != nil {
			f.fail(w, e.status, e.code, e.msg)
			return
		}
		take := func(from *[]api.Invitation) (api.Invitation, bool) {
			for i, inv := range *from {
				if inv.ID == id {
					*from = append((*from)[:i:i], (*from)[i+1:]...)
					return inv, true
				}
			}
			return api.Invitation{}, false
		}
		gone := func() { f.fail(w, http.StatusNotFound, "not_found", "Not found") }
		switch action {
		case "accept":
			inv, ok := take(&f.pending)
			if !ok {
				gone()
				return
			}
			joined := api.List{ID: inv.List.ID, Name: inv.List.Name, Role: "member"}
			f.lists = append(f.lists, joined)
			now := time.Now()
			f.todos = append(f.todos, api.Todo{
				ID: "joined-" + id, ListID: joined.ID, Title: "Already in " + joined.Name,
				CreatedAt: now, UpdatedAt: now,
			})
			write(w, map[string]any{"list": joined})
		case "decline":
			inv, ok := take(&f.pending)
			if !ok {
				gone()
				return
			}
			f.declined = append(f.declined, inv)
			write(w, map[string]any{"ok": true})
		case "restore":
			inv, ok := take(&f.declined)
			if !ok {
				gone()
				return
			}
			f.pending = append([]api.Invitation{inv}, f.pending...)
			write(w, map[string]any{"invitation": inv})
		}
	})
	mux.HandleFunc("/people", func(w http.ResponseWriter, r *http.Request) {
		if e := f.peopleErr; e != nil {
			f.fail(w, e.status, e.code, e.msg)
			return
		}
		write(w, map[string]any{"people": f.people})
	})
	mux.HandleFunc("/lists", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lists = append(f.lists, api.List{
				ID: "l_new", Name: body.Name, Role: "owner",
			})
		}
		write(w, map[string]any{"lists": f.lists})
	})
	mux.HandleFunc("/todos", func(w http.ResponseWriter, r *http.Request) {
		// A request carrying since is the change feed, which has a
		// different body shape from a filtered list.
		if since := r.URL.Query().Get("since"); since != "" || r.Method == http.MethodGet {
			if since != "" {
				f.feeds++
				// Like the real one: everything updated since the cursor.
				changes := []api.SyncTodo{}
				if f.stubFeed {
					changes = append(changes, api.SyncTodo{Todo: api.Todo{ID: "t_feed", ListID: "l1", Title: "from the feed", UpdatedAt: time.Now()}})
				}
				if ms, err := strconv.ParseInt(since, 10, 64); err == nil {
					cursor := time.UnixMilli(ms)
					for _, todo := range f.todos {
						if todo.UpdatedAt.After(cursor) {
							changes = append(changes, api.SyncTodo{Todo: todo, Checklist: f.steps[todo.ID]})
						}
					}
				}
				write(w, map[string]any{
					"todos":   changes,
					"lists":   f.lists,
					"cursor":  fakeCursor(),
					"hasMore": false,
				})
				return
			}
			// Filtered list: honour status, search and undated the way the
			// real service does. Undated todos are only ever in "open" and
			// "all", never in "today", and reproducing that is what keeps
			// the tests honest about a todo created with no date.
			f.loads++
			f.queries = append(f.queries, r.URL.Query())
			q := strings.ToLower(r.URL.Query().Get("q"))
			undated := r.URL.Query().Get("undated") == "1"
			status := r.URL.Query().Get("status")
			now := time.Now()
			if f.clock != nil {
				loc, err := time.LoadLocation(f.tz)
				if err != nil {
					loc = time.UTC
				}
				now = f.clock().In(loc)
			}
			today := now.Format("2006-01-02")
			me := f.userID
			if me == "" {
				me = "u1"
			}
			out := []api.Todo{}
			for _, todo := range f.todos {
				if q != "" && !strings.Contains(strings.ToLower(todo.Title+" "+deref(todo.Notes)), q) {
					continue
				}
				if id := r.URL.Query().Get("listId"); id != "" && todo.ListID != id {
					continue
				}
				switch who := r.URL.Query().Get("assigned"); who {
				case "":
				case "anyone":
					if todo.AssigneeID != nil {
						continue
					}
				default:
					if who == "me" {
						who = me
					}
					if todo.AssigneeID == nil || *todo.AssigneeID != who {
						continue
					}
				}
				if undated && todo.ScheduledDay != nil {
					continue
				}
				dated := todo.ScheduledDay != nil
				isToday := dated && *todo.ScheduledDay == today
				switch status {
				case "", "open":
					if todo.IsCompleted {
						continue
					}
				case "today":
					if !isToday {
						continue
					}
				case "completed":
					if !todo.IsCompleted {
						continue
					}
				case "overdue":
					if todo.DueAt == nil || !todo.DueAt.Before(now) {
						continue
					}
				case "upcoming":
					if !dated || *todo.ScheduledDay <= today {
						continue
					}
				}
				out = append(out, todo)
			}
			page, next, ok := pageOf(r.URL.Query(), out)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				write(w, map[string]any{"error": "invalid request", "code": api.CodeInvalidRequest})
				return
			}
			write(w, map[string]any{"todos": page, "nextCursor": next})
			return
		}
		if r.Method == http.MethodPost {
			var body api.NewTodo
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.creates = append(f.creates, body)
			f.writes++
			f.created = append(f.created, body)
			todo := api.Todo{
				ID: "t_new", ListID: "l1", Title: body.Title,
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}
			f.todos = append([]api.Todo{todo}, f.todos...)
			w.WriteHeader(http.StatusCreated)
			write(w, map[string]any{"todos": []api.Todo{todo}})
			return
		}
		write(w, map[string]any{"todos": f.todos})
	})
	mux.HandleFunc("/todos/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/todos/")
		parts := strings.Split(path, "/")
		id := parts[0]
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}
		find := func() *api.Todo {
			for i := range f.todos {
				if f.todos[i].ID == id {
					return &f.todos[i]
				}
			}
			return nil
		}
		switch action {
		case "checklist":
			if r.Method == http.MethodPost {
				var body struct{ Title string }
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.steps[id] = append(f.steps[id], api.ChecklistItem{
					ID: fmt.Sprintf("c%d", len(f.steps[id])+10), TodoID: id,
					Title: body.Title, Position: len(f.steps[id]),
				})
				w.WriteHeader(http.StatusCreated)
				write(w, map[string]any{"item": f.steps[id][len(f.steps[id])-1]})
				return
			}
			f.stepReads = append(f.stepReads, id)
			write(w, map[string]any{"items": f.steps[id]})
		case "complete":
			if todo := find(); todo != nil {
				now := time.Now()
				todo.IsCompleted = true
				todo.CompletedAt = &now
				todo.UpdatedAt = now
			}
			todo := find()
			write(w, map[string]any{"todo": todo, "next": nil})
		case "uncomplete":
			if todo := find(); todo != nil {
				todo.IsCompleted = false
				todo.CompletedAt = nil
			}
			write(w, map[string]any{"todo": find()})
		case "claim", "release":
			f.assignment(w, find(), action == "claim")
		default:
			switch r.Method {
			case http.MethodGet:
				if todo := find(); todo != nil {
					write(w, map[string]any{"todo": todo})
				} else {
					w.WriteHeader(http.StatusNotFound)
					write(w, map[string]any{"error": "Not found", "code": "not_found"})
				}
			case http.MethodPatch:
				var patch map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&patch)
				todo := find()
				if todo == nil {
					w.WriteHeader(http.StatusNotFound)
					write(w, map[string]any{"error": "Not found", "code": "not_found"})
					return
				}
				f.writes++
				f.patches = append(f.patches, patchCall{id: id, body: patch})
				// Like the real one, so the change feed carries the edit.
				todo.UpdatedAt = time.Now()
				if raw, ok := patch["title"]; ok {
					_ = json.Unmarshal(raw, &todo.Title)
				}
				if raw, ok := patch["isCompleted"]; ok {
					_ = json.Unmarshal(raw, &todo.IsCompleted)
				}
				if raw, ok := patch["scheduledDay"]; ok && string(raw) != "null" {
					var day string
					_ = json.Unmarshal(raw, &day)
					todo.ScheduledDay = &day
				}
				if raw, ok := patch["notes"]; ok && string(raw) == "null" {
					todo.Notes = nil
				} else if ok {
					var s string
					_ = json.Unmarshal(raw, &s)
					todo.Notes = &s
				}
				write(w, map[string]any{"todo": todo, "next": nil})
			case http.MethodDelete:
				f.deletes = append(f.deletes, id)
				kept := f.todos[:0]
				for _, todo := range f.todos {
					if todo.ID != id {
						kept = append(kept, todo)
					}
				}
				f.todos = kept
				write(w, map[string]any{"ok": true})
			}
		}
	})
	mux.HandleFunc("/checklist/", func(w http.ResponseWriter, r *http.Request) {
		// Patch ticks or renames a step, delete removes it. The client
		// refetches the todo's steps afterwards, so the fixture must
		// actually change here.
		id := strings.TrimPrefix(r.URL.Path, "/checklist/")
		for todoID, items := range f.steps {
			for i, it := range items {
				if it.ID != id {
					continue
				}
				switch r.Method {
				case http.MethodPatch:
					var body struct {
						Title       *string    `json:"title"`
						CompletedAt *time.Time `json:"completedAt"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.Title != nil {
						items[i].Title = *body.Title
					}
					items[i].CompletedAt = body.CompletedAt
					write(w, map[string]any{"item": items[i]})
				case http.MethodDelete:
					f.steps[todoID] = append(items[:i], items[i+1:]...)
					write(w, map[string]any{"ok": true})
				}
				return
			}
		}
		f.fail(w, http.StatusNotFound, "not_found", "No such step")
	})
	mux.HandleFunc("/lists/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/members") && f.membersBy[strings.Split(strings.TrimPrefix(r.URL.Path, "/lists/"), "/")[0]] != nil:
			write(w, map[string]any{"members": f.membersBy[strings.Split(strings.TrimPrefix(r.URL.Path, "/lists/"), "/")[0]]})
		case strings.HasSuffix(r.URL.Path, "/members"):
			if len(f.members) == 0 {
				f.members = []api.Member{
					{UserID: "u1", Email: "demo@example.com", Role: "owner"},
					{UserID: "u2", Email: "sam@example.com", Role: "member"},
				}
			}
			write(w, map[string]any{"members": f.members})
		case strings.HasSuffix(r.URL.Path, "/invitations"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.invites = append(f.invites, body)
			if e := f.inviteErr; e != nil {
				if e.retryAfter != "" {
					w.Header().Set("Retry-After", e.retryAfter)
				}
				f.fail(w, e.status, e.code, e.msg)
				return
			}
			w.WriteHeader(http.StatusCreated)
			invitee := map[string]any{"userId": "u9", "username": nil}
			if name := body["username"]; name != "" {
				invitee["username"] = strings.TrimPrefix(name, "@")
			}
			write(w, map[string]any{"ok": true, "invitee": invitee})
		case strings.HasSuffix(r.URL.Path, "/invites"):
			w.WriteHeader(http.StatusCreated)
			write(w, map[string]any{"inviteUrl": "https://evado.example/inv/abc", "expiresIn": 604800})
		default:
			if r.Method == http.MethodDelete {
				// /lists/{id} and /lists/{id}/members/{userId}
				id := strings.Split(strings.TrimPrefix(r.URL.Path, "/lists/"), "/")[0]
				kept := f.lists[:0]
				for _, l := range f.lists {
					if l.ID != id {
						kept = append(kept, l)
					}
				}
				f.lists = kept
			}
			write(w, map[string]any{"ok": true})
		}
	})
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/parse", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title string
			Smart *bool
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.parseAnswer != nil {
			write(w, map[string]any{"parsed": f.parseAnswer(body.Title, body.Smart)})
			return
		}
		if f.parsed != nil {
			write(w, f.parsed)
			return
		}
		write(w, map[string]any{
			"title":        body.Title,
			"scheduledDay": "2026-03-10",
			"doAt":         "2026-03-10T15:00:00Z",
			"hasTime":      true,
		})
	})
	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		feed := []api.SyncTodo{{Todo: api.Todo{ID: "t1", Title: "from the feed", ListID: "l1", UpdatedAt: time.Now()}}}
		var body api.SyncRequest
		if r.ContentLength > 0 {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		switch {
		case body.Since != "":
			feed = []api.SyncTodo{{
				Todo: api.Todo{ID: "t_feed", Title: "changed since the cursor", ListID: "l1", UpdatedAt: time.Now()},
			}}
		case !f.stubFeed:
			// An initial sync is how the Todos tab reads the whole account
			// from the service, so it counts as a load.
			f.loads++
			feed = nil
			for _, todo := range f.todos {
				feed = append(feed, api.SyncTodo{Todo: todo, Checklist: f.steps[todo.ID]})
			}
		}
		write(w, map[string]any{
			"todos":   feed,
			"lists":   f.lists,
			"cursor":  fakeCursor(),
			"hasMore": false,
		})
	})
	mux.HandleFunc("/notification-channels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		write(w, map[string]any{"error": "Session required", "code": "session_required"})
	})
	mux.HandleFunc("/tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		write(w, map[string]any{"error": "Session required", "code": "session_required"})
	})
	mux.HandleFunc("/import", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		dry := r.URL.Query().Get("dryRun") == "true"
		f.importCalls = append(f.importCalls, dry)
		f.importBodies = append(f.importBodies, raw)
		write(w, api.ImportResult{DryRun: dry, Added: 1, Complete: !f.importIncomplete, Remaining: 3, Dropped: map[string]int{"assignee": 1}})
	})
	mux.HandleFunc("/me/export/todos", func(w http.ResponseWriter, r *http.Request) {
		f.portableExports++
		_ = api.EncodeTodosExport(w, api.Export{ExportedAt: time.Now(), Todos: []api.SyncTodo{}, Lists: []api.ListSnapshot{}})
	})
	mux.HandleFunc("/me/export", func(w http.ResponseWriter, r *http.Request) {
		f.accountCalls++
		if f.noAccountScope {
			f.deny(w)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		write(w, map[string]any{"error": "Forbidden", "code": "forbidden"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.refuse != nil && f.refuse(r) {
			f.fail(w, http.StatusInternalServerError, "internal", "the server fell over")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// deny is the service's answer to a token without the account scope.
func (f *fakeAPI) deny(w http.ResponseWriter) {
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": "token lacks the account scope", "code": "insufficient_scope", "scope": "account",
	})
}

// harness runs the model headlessly and drains command results, so a test
// can assert on the rendered output after a realistic sequence of events.
type harness struct {
	t     *testing.T
	model *Model
	cmds  []tea.Cmd
}

// TestMain speeds up the shared clock so toasts and spinners do not make
// the suite wait on real time.
func TestMain(m *testing.M) {
	tickInterval = time.Millisecond
	pollOverride = time.Millisecond
	blinkSpeed = time.Microsecond
	// Animations and the live date preview run on real time, so they are
	// turned off; the tests that cover them turn them back on.
	frameInterval, animDur, parseDebounce, syncDelay, peekDelay = 0, 0, 0, 0, 0
	// Point the config at a throwaway file for the whole package. The app
	// persists the last filter, and a test run must never touch the real
	// one in the user's home directory.
	dir, err := os.MkdirTemp("", "evado-tui-test")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Setenv("EVADO_CONFIG", filepath.Join(dir, "config.json")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func newHarness(t *testing.T, f *fakeAPI) *harness {
	t.Helper()
	return newHarnessWith(t, f, nil)
}

// newHarnessWith is newHarness with a chance to adjust the config first.
func newHarnessWith(t *testing.T, f *fakeAPI, tweak func(*config.Config)) *harness {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		Token:   "evt_test",
		BaseURL: srv.URL,
		Status:  api.StatusAll,
		NoCache: true, // tests must not share a snapshot on disk
	}
	if tweak != nil {
		tweak(&cfg)
	}
	m := New(context.Background(), cfg)
	m.ready = true
	h := &harness{t: t, model: m}
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.run(m.Init())
	return h
}

// send delivers a message and runs the commands it returns.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	next, cmd := h.model.Update(msg)
	h.model = next.(*Model)
	if cmd != nil {
		h.cmds = append(h.cmds, cmd)
	}
}

// run executes commands, feeding each resulting message back in, until
// nothing is left. Bubbletea commands are plain functions, so this
// reproduces the runtime loop closely enough for assertions.
func (h *harness) run(cmds ...tea.Cmd) {
	h.t.Helper()
	queue := append([]tea.Cmd{}, cmds...)
	// Bound the loop so a self-rescheduling command cannot hang a test.
	for i := 0; i < 200 && len(queue) > 0; i++ {
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if msg == nil {
			continue
		}
		// Batch and sequence messages wrap more commands; unwrap them so
		// the loop keeps making progress the way the runtime would.
		switch inner := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, inner...)
			continue
		}
		// Ticks only drive clock refreshes; drop them to avoid looping.
		switch msg.(type) {
		case tickMsg, pollMsg:
			continue
		}
		next, c := h.model.Update(msg)
		h.model = next.(*Model)
		if c != nil {
			queue = append(queue, c)
		}
	}
}

// press types keys one at a time, draining the commands each one produces
// before the next arrives, the way the real runtime does.
func (h *harness) press(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		msg := tea.KeyMsg{Type: keyType(k)}
		if msg.Type == tea.KeyRunes {
			msg.Runes = []rune(k)
		}
		h.send(msg)
		h.run(h.cmds...)
		h.cmds = nil
	}
}

func keyType(s string) tea.KeyType {
	switch s {
	case "enter":
		return tea.KeyEnter
	case "esc":
		return tea.KeyEsc
	case "tab":
		return tea.KeyTab
	case "up":
		return tea.KeyUp
	case "down":
		return tea.KeyDown
	case "right":
		return tea.KeyRight
	case "left":
		return tea.KeyLeft
	case "space":
		return tea.KeySpace
	case "ctrl+c":
		return tea.KeyCtrlC
	case "bs":
		return tea.KeyBackspace
	case "shift+tab":
		return tea.KeyShiftTab
	case "ctrl+s":
		return tea.KeyCtrlS
	case "ctrl+u":
		return tea.KeyCtrlU
	default:
		return tea.KeyRunes
	}
}

func (h *harness) view() string { return h.model.View() }

func (h *harness) mustContain(s string) {
	h.t.Helper()
	if !strings.Contains(h.view(), s) {
		h.t.Errorf("view missing %q\n---\n%s", s, h.view())
	}
}

func (h *harness) mustNotContain(s string) {
	h.t.Helper()
	if strings.Contains(h.view(), s) {
		h.t.Errorf("view should not contain %q\n---\n%s", s, h.view())
	}
}

func sampleAPI() *fakeAPI {
	day := "2026-03-10"
	past := time.Now().Add(-48 * time.Hour)
	notes := "check the meter reading first"
	return &fakeAPI{
		lists: []api.List{
			{ID: "l1", Name: "Personal", IsPersonal: true, Role: "owner"},
			{ID: "l2", Name: "Household", Role: "owner"},
			{ID: "l3", Name: "Work", Role: "member", ReadOnly: true},
		},
		todos: []api.Todo{
			{
				ID: "t1", ListID: "l1", Title: "Pay the cable bill",
				ScheduledDay: &day, HasTime: true,
				Notes:     &notes,
				UpdatedAt: past,
				CreatedAt: past,
			},
			{
				ID: "t2", ListID: "l2", Title: "Renew passport",
				UpdatedAt: past, CreatedAt: past,
			},
			{
				ID: "t3", ListID: "l1", Title: "Buy milk",
				IsCompleted: true, UpdatedAt: past, CreatedAt: past,
			},
		},
		steps: map[string][]api.ChecklistItem{
			"t1": {
				{ID: "c1", TodoID: "t1", Title: "read the meter"},
				{ID: "c2", TodoID: "t1", Title: "call the provider", CompletedAt: &past},
			},
		},
	}
}

func TestTodosViewRendersRows(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.mustContain("Pay the cable bill")
	h.mustContain("Renew passport")
	h.mustContain("Buy milk")
	h.mustContain("Personal")
	h.mustContain("3 todos")
}

func TestLoginScreenWithoutToken(t *testing.T) {
	m := New(context.Background(), config.Config{})
	if m.layer != layerLogin {
		t.Fatalf("layer = %v, want login", m.layer)
	}
	m.ready = true
	h := &harness{t: t, model: m}
	h.send(tea.WindowSizeMsg{Width: 90, Height: 30})
	h.press("down", "enter")
	if !strings.Contains(h.view(), "evt_") {
		t.Errorf("the token step should mention the token prefix:\n%s", h.view())
	}
}

func TestEnteringDetailShowsNotesAndChecklist(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("j") // select the second todo, which has no steps
	h.press("enter")
	if h.model.focus != focusDetail {
		t.Fatalf("focus = %v, want detail", h.model.focus)
	}
	h.mustContain("checklist")

	h.press("esc", "g") // back to the top row, then open the first todo
	h.press("enter")
	h.mustContain("check the meter reading first")
	h.mustContain("read the meter")
	h.mustContain("call the provider")
}

func TestOpeningDetailReadsTheChecklistOnce(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	f.stepReads = nil
	h.press("enter")
	h.mustContain("read the meter")
	if len(f.stepReads) != 1 || f.stepReads[0] != "t1" {
		t.Errorf("steps read = %v, want one read of t1", f.stepReads)
	}
}

// A change made on another device shows in the open detail pane when the
// list hears of it, through the change feed or a fresh read, without the
// pane being opened again.
func TestOpenDetailFollowsChangesMadeElsewhere(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "enter")
	if h.model.focus != focusDetail || h.model.todos.detail.ID != "t1" {
		t.Fatalf("focus = %v on %q, want t1 open", h.model.focus, h.model.todos.detail.ID)
	}

	changed := f.todos[0]
	changed.Title = "Pay the cable bill online"
	h.send(feedMsg{seq: h.model.todos.seq, cursor: "next", changes: []api.SyncTodo{{Todo: changed}}})
	if got := h.model.todos.detail.Title; got != changed.Title {
		t.Errorf("after the feed the pane shows %q, want %q", got, changed.Title)
	}

	f.todos[0].Title = "Pay the cable bill by card"
	h.press("r")
	if got := h.model.todos.detail.Title; got != f.todos[0].Title {
		t.Errorf("after a refresh the pane shows %q, want %q", got, f.todos[0].Title)
	}
	h.mustContain("Pay the cable bill by card")

	// A copy older than the one in the pane, from a read that was sent
	// before the last change, leaves the pane as it is.
	stale := h.model.todos.detail
	stale.Title = "Pay the cable bill"
	stale.UpdatedAt = h.model.todos.detail.UpdatedAt.Add(-time.Minute)
	h.send(feedMsg{seq: h.model.todos.seq, cursor: "later", changes: []api.SyncTodo{{Todo: stale}}})
	if got := h.model.todos.detail.Title; got != f.todos[0].Title {
		t.Errorf("an older copy put back %q in the pane, want %q", got, f.todos[0].Title)
	}
}

// paneClosedOnDelete checks that the open todo's pane was closed because it
// was deleted somewhere else, with the list back in focus without it.
func paneClosedOnDelete(t *testing.T, h *harness, id string) {
	t.Helper()
	if h.model.focus != focusList || h.model.todos.detail.ID != "" {
		t.Errorf("focus = %v on %q, want the list with the pane closed", h.model.focus, h.model.todos.detail.ID)
	}
	if h.model.toast == nil || h.model.toast.text != "that todo was deleted elsewhere" {
		t.Errorf("toast = %+v, want one saying the todo was deleted elsewhere", h.model.toast)
	}
	for _, it := range h.model.todos.items {
		if it.ID == id {
			t.Errorf("%s is still on the list", id)
		}
	}
}

// A todo deleted on another device while it is open closes its pane when the
// change feed says so. A change to it that is not a delete leaves it open.
func TestOpenTodoDeletedElsewhereClosesThePane(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "enter")
	if h.model.focus != focusDetail || h.model.todos.detail.ID != "t1" {
		t.Fatalf("focus = %v on %q, want t1 open", h.model.focus, h.model.todos.detail.ID)
	}

	changed := f.todos[0]
	changed.Title = "Pay the cable bill online"
	changed.UpdatedAt = time.Now()
	h.send(feedMsg{seq: h.model.todos.seq, cursor: "next", changes: []api.SyncTodo{{Todo: changed}}})
	h.run(h.cmds...)
	h.cmds = nil
	if h.model.focus != focusDetail || h.model.todos.detail.ID != "t1" {
		t.Fatalf("a change that is not a delete closed the pane: focus = %v on %q", h.model.focus, h.model.todos.detail.ID)
	}

	gone := changed
	at := time.Now()
	gone.DeletedAt = &at
	h.send(feedMsg{seq: h.model.todos.seq, cursor: "later", changes: []api.SyncTodo{{Todo: gone}}})
	h.run(h.cmds...)
	h.cmds = nil
	paneClosedOnDelete(t, h, "t1")
	h.mustContain("that todo was deleted elsewhere")
}

// A read of the whole account that no longer has the open todo closes its
// pane too, both when it is the list on screen and when it is the snapshot
// a narrowed list keeps the counts from. A read that still has it does not.
func TestOpenTodoMissingFromAFullReadClosesThePane(t *testing.T) {
	for _, tc := range []struct {
		name   string
		narrow func(*config.Config)
	}{
		{"every list", nil},
		{"one list", func(c *config.Config) { c.ListID = "l1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sampleAPI()
			h := newHarnessWith(t, f, tc.narrow)
			fullRead := func() {
				// With no cursor to follow, the next refresh reads the
				// whole account rather than the change feed.
				h.model.todos.cursor, h.model.todos.snapCursor = "", ""
				h.press("r")
			}
			h.press("g", "enter")
			if h.model.focus != focusDetail || h.model.todos.detail.ID != "t1" {
				t.Fatalf("focus = %v on %q, want t1 open", h.model.focus, h.model.todos.detail.ID)
			}

			fullRead()
			if h.model.focus != focusDetail || h.model.todos.detail.ID != "t1" {
				t.Fatalf("a read that still has t1 closed the pane: focus = %v on %q", h.model.focus, h.model.todos.detail.ID)
			}

			f.todos = f.todos[1:]
			fullRead()
			paneClosedOnDelete(t, h, "t1")
		})
	}
}

// A delete made here closes the pane the way it always has, with its own
// receipt, even when the change feed reports the delete before the answer
// to it arrives.
func TestDeletingTheOpenTodoHereIsUnchanged(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "enter", "d")
	h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	gone := f.todos[0]
	at := time.Now()
	gone.DeletedAt = &at
	h.send(feedMsg{seq: h.model.todos.seq, cursor: "next", changes: []api.SyncTodo{{Todo: gone}}})
	if h.model.focus != focusDetail || h.model.todos.detail.ID != "t1" {
		t.Fatalf("the feed closed the pane while the delete made here was on its way: focus = %v on %q", h.model.focus, h.model.todos.detail.ID)
	}
	cmds := h.cmds
	h.cmds = nil
	h.run(cmds...)

	if h.model.focus != focusList || h.model.todos.detail.ID != "" {
		t.Errorf("focus = %v on %q, want the list with the pane closed", h.model.focus, h.model.todos.detail.ID)
	}
	if h.model.toast == nil || !strings.HasPrefix(h.model.toast.text, `deleted "Pay the cable bill"`) {
		t.Errorf("toast = %+v, want the receipt for the delete made here", h.model.toast)
	}
	if len(f.deletes) != 1 {
		t.Errorf("deletes = %v, want one", f.deletes)
	}
}

func TestCompleteAndReopen(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "space") // complete the first todo
	if !f.todos[0].IsCompleted {
		t.Error("space should complete the selected todo")
	}
	h.press("space")
	if f.todos[0].IsCompleted {
		t.Error("space again should reopen it")
	}
}

func TestCompleteWithRecurrenceReportsNext(t *testing.T) {
	f := sampleAPI()
	// A recurring todo returns its next occurrence, which the UI reports.
	day := "2026-03-17"
	rule := "1:week"
	f.todos[0].RecurrenceRule = &rule
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") {
			_, _ = w.Write([]byte(`{"todo":{"id":"t1","title":"Pay the cable bill"},"next":{"id":"t_next","title":"Pay the cable bill","scheduledDay":"2026-03-17"}}`))
			return
		}
		f.handler(t).ServeHTTP(w, r)
	}))
	defer srv.Close()

	h := &harness{t: t}
	h.model = New(context.Background(), config.Config{Token: "evt_test", BaseURL: srv.URL, Status: api.StatusAll, NoCache: true})
	h.model.ready = true
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.run(h.model.Init())
	h.press("space")
	_ = day
	if !strings.Contains(h.model.toastText(), "next one is") {
		t.Errorf("toast = %q, want a mention of the next occurrence", h.model.toastText())
	}
}

func TestDeleteTodoAsksFirst(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("d")
	if h.model.layer != layerConfirm {
		t.Fatalf("layer = %v, want a confirmation", h.model.layer)
	}
	if len(f.deletes) != 0 {
		t.Error("nothing should be deleted before confirming")
	}
	h.press("y")
	if len(f.deletes) != 1 || f.deletes[0] != "t1" {
		t.Errorf("deletes = %v, want t1", f.deletes)
	}
}

// A refused create or delete says which it was, not that loading failed.
func TestFailedCreateAndDeleteSayWhatFailed(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	f.refuse = refusing(http.MethodPost, "/todos")
	h.press("n")
	h.typeText("Call the bank")
	h.press("enter")
	if got := h.model.toastText(); !strings.HasPrefix(got, "add todo: ") {
		t.Errorf("toast = %q, want it to say adding the todo failed", got)
	}

	f.refuse = refusing(http.MethodDelete, "/todos/t1")
	h.press("esc", "g", "d", "y")
	if got := h.model.toastText(); !strings.HasPrefix(got, "delete todo: ") {
		t.Errorf("toast = %q, want it to say deleting the todo failed", got)
	}
	h.mustContain("Pay the cable bill")
}

func TestEscapeClosesConfirmation(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("d", "esc")
	if len(f.deletes) != 0 {
		t.Errorf("escape should cancel, got deletes %v", f.deletes)
	}
	if h.model.layer != layerNone {
		t.Errorf("layer = %v, want none", h.model.layer)
	}
}

// A question that replaces one left unanswered brings its own answers:
// turning down the new one must not run what saying no to the old one
// would have done.
func TestReplacedQuestionDoesNotKeepItsNo(t *testing.T) {
	h := newHarness(t, sampleAPI())
	ranNo := false
	h.model.askOr("first", "First?", "", false, nil, func() tea.Cmd {
		ranNo = true
		return nil
	})
	h.model.ask("second", "Second?", "", false, nil)
	h.press("esc")
	if ranNo {
		t.Error("turning down the second question ran the first one's no")
	}
	if h.model.layer != layerNone {
		t.Errorf("layer = %v, want the dialog closed", h.model.layer)
	}
}

func TestDangerousConfirmationNeedsTwoEnters(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("2") // Lists tab
	h.press("j") // Household, not personal
	h.press("d")
	if h.model.layer != layerConfirm {
		t.Fatalf("no confirmation for a dangerous delete")
	}
	h.press("enter") // arms
	if len(f.lists) != 3 {
		t.Error("the first enter should only arm the prompt")
	}
	if h.model.layer != layerConfirm {
		t.Error("the dialog should stay open after arming")
	}
	h.press("enter") // commits
	if len(f.lists) != 2 {
		t.Errorf("lists = %d, want the list deleted", len(f.lists))
	}
}

func TestPersonalListCannotBeDeleted(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("2", "d")
	if h.model.layer == layerConfirm {
		t.Error("the personal list must not offer a delete prompt")
	}
	if !strings.Contains(h.model.toastText(), "personal") {
		t.Errorf("toast = %q, want an explanation", h.model.toastText())
	}
}

func TestNewTodoFlow(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("n")
	if h.model.layer != layerForm || h.model.form == nil {
		t.Fatalf("layer = %v, want a form", h.model.layer)
	}
	// The form starts on the title field; type then save.
	h.press("W", "i", "p", "e", " ", "r", "e", "n", "t", " ", "o", "i", "l")
	h.press("enter") // enter saves the new-todo form
	if f.writes != 1 {
		t.Fatalf("writes = %d, want 1", f.writes)
	}
	if !strings.Contains(f.todos[0].Title, "Wipe rent oil") {
		t.Errorf("created title = %q", f.todos[0].Title)
	}
}

func TestEditOnlySendsChangedFields(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("e")
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want a form", h.model.layer)
	}
	// Change nothing and save: no request should go out.
	h.press("enter")
	if f.writes != 0 {
		t.Errorf("writes = %d, want 0 when nothing changed", f.writes)
	}
	if !strings.Contains(h.model.toastText(), "nothing changed") {
		t.Errorf("toast = %q", h.model.toastText())
	}
}

func TestClearDateSendsNull(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("e")
	// Move to the "do day" field and clear it.
	for i := 0; i < 3; i++ {
		h.press("tab")
	}
	for i := 0; i < 12; i++ {
		h.press("bs")
	}
	h.press("enter")
	if f.writes == 0 {
		t.Fatal("clearing a date should send a patch")
	}
}

// On a short terminal the edit form scrolls, and each field tab reaches
// has to be drawn, with the focus marker on it.
func TestShortTerminalFormScrollsToFocusedField(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 24})
	h.press("e")
	if h.model.layer != layerForm || !h.model.form.scrollable() {
		t.Fatalf("want a scrolling form at height 24, layer = %v", h.model.layer)
	}
	labels := []string{"list", "notes", "do day", "do at", "due", "remind", "repeat", "done"}
	for _, label := range labels {
		h.press("tab")
		focused := ""
		for _, line := range strings.Split(stripANSI(h.view()), "\n") {
			if strings.Contains(line, "▌ ") {
				focused = line
			}
		}
		if !strings.Contains(focused, "▌ "+label) {
			t.Fatalf("after tabbing to %q the focused row is %q\n---\n%s", label, focused, stripANSI(h.view()))
		}
	}
	// The last field brings the closing note into view, and the hint
	// stops promising more below.
	h.mustContain("Empty date fields clear them")
	h.mustContain("more fields above")
	h.mustNotContain("more fields below")
	// Wrapping back to the top shows the title again.
	h.press("tab")
	h.mustContain("more fields below")
	if v := stripANSI(h.view()); !strings.Contains(v, "▌ title") {
		t.Errorf("title is not focused after wrapping\n---\n%s", v)
	}
}

func TestSearchFiltersTodos(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("/")
	if h.model.layer != layerSearch {
		t.Fatalf("layer = %v, want search", h.model.layer)
	}
	h.press("p", "a", "s", "s", "enter")
	if h.model.todos.filter.query != "pass" {
		t.Errorf("query = %q", h.model.todos.filter.query)
	}
	h.mustNotContain("Pay the cable bill")
}

func TestParsePreview(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("p")
	if h.model.todos.parsePreview == nil {
		t.Fatal("no parse preview after pressing p")
	}
	h.mustContain("how that reads")
}

func TestCycleStatusFilter(t *testing.T) {
	h := newHarness(t, sampleAPI())
	start := h.model.todos.filter.status
	h.press("f")
	if h.model.todos.filter.status == start {
		t.Error("f should cycle the status filter")
	}
}

func TestListsViewShowsListsAndInvite(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("2")
	h.mustContain("Household")
	h.mustContain("Work")

	h.press("i", "enter") // create an invite link and confirm
	if h.model.listsV.invite == nil {
		t.Fatal("no invite was created")
	}
	if h.model.listsV.invite.InviteURL == "" {
		t.Error("invite url is empty")
	}
	// The heading in the list's pane, not the clipboard receipt, which
	// reads differently on a machine with no clipboard program.
	h.mustContain("INVITE LINK")
	h.mustContain("https://evado.example/inv/abc")
	_ = f
}

func TestListMembersLoad(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("2", "m")
	if !h.model.listsV.showMembers || len(h.model.listsV.members) == 0 {
		t.Fatal("members were not loaded")
	}
	h.mustContain("demo@example.com")
}

func TestSyncViewPullsAndShowsFeed(t *testing.T) {
	f := sampleAPI()
	f.stubFeed = true
	h := newHarness(t, f)
	h.press("3")
	if len(h.model.syncV.feed) == 0 {
		t.Fatal("initial sync returned nothing")
	}
	h.mustContain("from the feed")
	if h.model.syncV.cursor == "" {
		t.Error("a cursor should be kept after a full pull")
	}
	// A second pull uses the cursor and asks for incremental changes.
	h.press("p")
	if len(h.model.syncV.feed) == 0 {
		t.Error("the second pull returned nothing")
	}
	if h.model.syncV.pulls < 2 {
		t.Errorf("pulls = %d, want at least 2", h.model.syncV.pulls)
	}
}

func TestAccountViewSections(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("4")
	h.mustContain("demo@example.com")

	h.press("j", "j", "j", "j", "j") // tokens
	h.mustContain("WEB APP ONLY")
	h.press("enter") // tries to list tokens; the API answers 403
	if !strings.Contains(h.model.toastText(), "session") {
		t.Errorf("toast = %q, want the session-only explanation", h.model.toastText())
	}
}

func TestAccountDeleteRequiresEmailMatch(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("4")
	h.model.acct.selectRow("delete")
	h.press("enter") // open the delete form
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want the delete form", h.model.layer)
	}
	h.press("w", "r", "o", "n", "g")
	h.press("enter")
	if h.model.layer == layerConfirm {
		t.Error("a wrong email must not reach the confirmation")
	}
	h.mustNotContain("deleted")
}

func TestHelpOverlay(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("?")
	if h.model.layer != layerHelp {
		t.Fatalf("layer = %v, want help", h.model.layer)
	}
	h.mustContain("narrows as you type")
	h.press("esc")
	if h.model.layer != layerNone {
		t.Errorf("layer = %v, want none after esc", h.model.layer)
	}
}

func TestTabSwitching(t *testing.T) {
	h := newHarness(t, sampleAPI())
	for _, k := range []string{"2", "3", "4", "1"} {
		h.press(k)
	}
	if h.model.tab != tabTodos {
		t.Errorf("tab = %v, want todos", h.model.tab)
	}
	h.mustContain("Pay the cable bill")
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		h := newHarness(t, sampleAPI())
		h.press(k)
		if !h.model.quited {
			t.Errorf("%q should quit", k)
		}
	}
}

func TestHelpOverlayFitsTerminal(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("?")
	for _, line := range strings.Split(h.view(), "\n") {
		if n := utf8.RuneCountInString(stripANSI(line)); n > 100 {
			t.Fatalf("line is %d cells wide, want at most 100: %q", n, line)
		}
	}
}

func TestNarrowTerminalDoesNotPanic(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {20, 8}, {200, 60}, {1, 1}} {
		m := New(context.Background(), config.Config{Token: "evt_test", Status: api.StatusAll, NoCache: true})
		m.ready = true
		h := &harness{t: t, model: m}
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		h.run(m.Init())
		for _, k := range []string{"enter", "tab", "j", "esc", "2", "3", "4", "?", "esc", "n", "esc"} {
			h.send(tea.KeyMsg{Type: keyType(k), Runes: []rune(k)})
		}
		_ = h.view()
	}
}

// ansiPattern matches the escape sequences lipgloss emits for colour.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI removes colour codes so a test can measure display cells.
func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func TestTabInsideDetailMovesToChecklistNotTab(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("g", "enter")
	if h.model.focus != focusDetail {
		t.Fatalf("focus = %v, want detail", h.model.focus)
	}
	// Tab must stay inside the todo rather than jumping to the next tab.
	h.press("tab")
	if h.model.tab != tabTodos {
		t.Errorf("tab = %v, want todos: tab belongs to the todo pane here", h.model.tab)
	}
	if h.model.focus != focusChecklist {
		t.Errorf("focus = %v, want checklist", h.model.focus)
	}
	// The detail stays on screen while the checklist has the keyboard.
	h.mustContain("CHECKLIST")
	h.mustContain("add step") // the checklist footer
	h.press("tab")
	if h.model.focus != focusDetail {
		t.Errorf("focus = %v, want detail after leaving the checklist", h.model.focus)
	}
	h.mustContain("assign") // the detail footer
	// And at the list level, tab does change tabs.
	h.press("esc", "tab")
	if h.model.tab != tabLists {
		t.Errorf("tab = %v, want lists", h.model.tab)
	}
}

func TestChecklistNavigationAndActions(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "enter", "tab")
	if len(h.model.todos.steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(h.model.todos.steps))
	}
	h.press("space")
	if f.steps["t1"][0].CompletedAt == nil {
		t.Error("space should tick the first step")
	}
	h.press("space")
	if f.steps["t1"][0].CompletedAt != nil {
		t.Error("space again should clear the tick")
	}
	h.press("j", "d", "y")
	if len(f.steps["t1"]) != 1 {
		t.Errorf("steps = %d, want 1 after deleting", len(f.steps["t1"]))
	}
}

func TestAddStepThroughTheForm(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "enter", "tab", "c")
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want a form", h.model.layer)
	}
	h.press("b", "o", "i", "l", " ", "m", "i", "l", "k")
	h.press("enter") // the step field is single-line, so enter saves
	if len(f.steps["t1"]) != 3 {
		t.Errorf("steps = %d, want 3", len(f.steps["t1"]))
	}
}

func TestJumpToInbox(t *testing.T) {
	h := newHarness(t, sampleAPI())
	// u moves the cursor to the inbox instead of filtering everything else
	// away, so the surrounding context stays on screen.
	h.press("u")
	sel := h.model.todos.selected()
	if sel == nil {
		t.Fatal("u should land on a todo")
	}
	if sel.ScheduledDay != nil {
		t.Errorf("cursor is on %q, want an undated todo", sel.Title)
	}
}

func TestJumpToInboxSaysWhenItIsEmpty(t *testing.T) {
	f := sampleAPI()
	f.todos = []api.Todo{{
		ID: "d1", ListID: "l1", Title: "Dated only",
		ScheduledDay: dayStr(0), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	h := newHarness(t, f)
	h.press("u")
	if !strings.Contains(strings.ToLower(h.model.toastText()), "inbox") {
		t.Errorf("toast = %q, want it to mention the inbox", h.model.toastText())
	}
}

func TestUncompleteFromDetail(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("G", "enter") // the completed todo
	if !h.model.todos.detail.IsCompleted {
		t.Skip("expected a completed todo at the bottom of the list")
	}
	h.press("space")
	if f.todos[2].IsCompleted {
		t.Error("space in the detail pane should reopen a completed todo")
	}
}

func TestReadOnlyListIsFlagged(t *testing.T) {
	h := newHarness(t, sampleAPI())
	// A frozen list is marked as such wherever it appears, and it cannot
	// be renamed, deleted or given new members.
	h.press("2", "G")
	h.mustContain("Work")
	h.mustContain("frozen")

	h.press("a")
	if h.model.layer == layerForm {
		t.Error("a frozen list must not accept new members")
	}
	h.press("d")
	if h.model.layer == layerConfirm {
		t.Error("a frozen list must not offer deletion")
	}
	h.press("e")
	if h.model.layer == layerForm {
		t.Error("a frozen list must not be renameable")
	}
}

func TestMemberPaneRemovesAMember(t *testing.T) {
	f := sampleAPI()
	// Two members so there is somebody other than the account owner.
	f.members = []api.Member{
		{UserID: "u1", Email: "demo@example.com", Role: "owner"},
		{UserID: "u2", Email: "sam@example.com", Role: "member"},
	}
	h := newHarness(t, f)
	h.press("2", "j", "m", "M") // Household → members → member pane
	if h.model.focus != focusMembers {
		t.Fatalf("focus = %v, want members", h.model.focus)
	}
	h.press("j", "d") // the second member, so it is a removal, not a leave
	if h.model.layer != layerConfirm {
		t.Fatal("removing somebody else should ask first")
	}
	// Removing another person is a destructive action, so the first
	// confirmation only arms the prompt.
	h.press("y")
	if h.model.layer != layerConfirm {
		t.Error("removing another member must take two confirmations")
	}
	h.press("y")
	if h.model.layer != layerNone {
		t.Error("the dialog should close after the second confirmation")
	}
	// Members are cached so the assignee picker can map emails to ids.
	if len(h.model.acct.membersFor("l2")) == 0 {
		t.Error("members should be cached for the assignee form")
	}
}

// Leaving a list reads the lists again, not the members of the list just
// left, which the service need no longer show. The pane that showed them
// closes.
func TestLeavingAListDoesNotReadItsMembersAgain(t *testing.T) {
	f := sampleAPI()
	f.members = []api.Member{
		{UserID: "u1", Email: "demo@example.com", Role: "member"},
		{UserID: "u2", Email: "sam@example.com", Role: "owner"},
	}
	h := newHarness(t, f)
	h.press("2", "j", "m", "M") // Household → members → member pane
	if h.model.focus != focusMembers {
		t.Fatalf("focus = %v, want members", h.model.focus)
	}
	f.refuse = func(r *http.Request) bool {
		return r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/members")
	}
	h.press("d") // the first member is the account itself
	if h.model.conf == nil || h.model.conf.title != "Leave this list?" {
		t.Fatalf("want the leave prompt, got %+v", h.model.conf)
	}
	h.press("y")
	if got := h.model.toastText(); got != "left the list" {
		t.Errorf("toast = %q, want word that the list was left", got)
	}
	if _, ok := h.model.listByID("l2"); ok {
		t.Error("the list just left is still shown")
	}
	if h.model.focus != focusList || h.model.listsV.showMembers {
		t.Errorf("focus = %v, members shown = %v; want the members pane closed", h.model.focus, h.model.listsV.showMembers)
	}
	if _, ok := h.model.acct.memberCache["l2"]; ok {
		t.Error("the members of the list just left are still kept")
	}
}

func TestAssignFormUsesCachedMembers(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("2", "j", "m") // load Household's members
	h.press("1", "j", "enter", "A")
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want the assign form", h.model.layer)
	}
	h.mustContain("demo@example.com")
	_ = f
}

func TestDeleteFromDetailReturnsToTheList(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("g", "enter") // open the first todo
	if h.model.focus != focusDetail {
		t.Fatal("the detail should be open")
	}
	h.press("d", "y") // delete and confirm
	if h.model.focus != focusList {
		t.Errorf("focus = %v, want the list after deleting", h.model.focus)
	}
	if h.model.todos.detail.ID != "" {
		t.Error("the detail should be cleared once the todo is gone")
	}
	if len(f.todos) != 2 {
		t.Errorf("todos = %d, want 2 after the delete", len(f.todos))
	}
	h.mustNotContain("id         t1")
}

func TestStatusFilterIsPersisted(t *testing.T) {
	h := newHarness(t, sampleAPI())
	before := h.model.todos.filter.status
	h.press("f")
	if h.model.todos.filter.status == before {
		t.Fatal("f should change the status")
	}
	if h.model.cfg.Status != h.model.todos.filter.status {
		t.Errorf("saved status = %q, want %q", h.model.cfg.Status, h.model.todos.filter.status)
	}
	// A new client picks the filter up again.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != h.model.todos.filter.status {
		t.Errorf("config on disk = %q, want %q", reloaded.Status, h.model.todos.filter.status)
	}
}

// The create forms are the first thing anyone touches, so the two ways to
// commit them are pinned down here.

func TestEnterSavesTheNewTodoForm(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("n")
	h.press("H", "i")
	h.press("enter")
	if h.model.layer != layerNone {
		t.Errorf("layer = %v, want the form to close on enter", h.model.layer)
	}
	if len(f.todos) != 4 || f.todos[0].Title != "Hi" {
		t.Errorf("newest todo = %+v, want one titled Hi", f.todos[0])
	}
}

func TestEnterIsANewlineInsideAProseBox(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("N")
	h.press("M", "i", "l", "k", "enter")
	if h.model.layer == layerNone {
		t.Fatal("enter must not save a box that is collecting several lines")
	}
	// The text and the newline are both still there.
	h.mustContain("Milk")
	h.press("ctrl+s")
	if h.model.layer != layerNone {
		t.Errorf("layer = %v, want ctrl+s to save", h.model.layer)
	}
	if len(f.todos) != 4 {
		t.Fatalf("todos = %d, want the new one", len(f.todos))
	}
	// The trailing newline is trimmed away, not sent to the server.
	if f.todos[0].Title != "Milk" {
		t.Errorf("title = %q, want Milk", f.todos[0].Title)
	}
}

func TestBoxedFieldIsActuallyRendered(t *testing.T) {
	// A field that holds the text but is not drawn reads to the user as
	// "typing does nothing", which is exactly the bug this guards.
	h := newHarness(t, sampleAPI())
	h.press("N", "M", "i", "l", "k")
	h.mustContain("Milk")
	h.mustContain("todo")
	h.press("N")
	h.press("S", "o", "m", "e", "t", "h", "i", "n", "g")
	h.mustContain("Something")
}

func TestSaveHintMatchesTheFocusedField(t *testing.T) {
	h := newHarness(t, sampleAPI())
	// Single-line title: enter saves.
	h.press("n")
	h.mustContain("enter save")
	h.press("esc", "N")
	// A prose box: the hint has to move to ctrl+s, or the user is stuck.
	h.mustContain("ctrl+s save")
	h.mustContain("enter adds a line")
}

func TestEveryFieldOfALongFormIsReachable(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("e")
	// The edit form is taller than the dialog, so it scrolls. Every field
	// must come into view as focus moves, and the last one must be the
	// "done" toggle.
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		f := h.model.form
		if f == nil {
			break
		}
		seen[f.fields[f.focus].label] = true
		h.press("tab")
	}
	for _, want := range []string{"title", "list", "notes", "do day", "do at", "due", "remind", "repeat", "done"} {
		if !seen[want] {
			t.Errorf("field %q never received focus; got %v", want, seen)
		}
	}
}

// A capture with no date is the common case, and it used to vanish: the
// default view only asked for "today", so an undated todo was never in the
// list and the app looked like it had thrown the work away.

func TestUndatedCaptureIsVisibleImmediately(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)

	h.press("N")
	h.press("C", "a", "l", "l", " ", "S", "a", "m", "ctrl+s")

	if !strings.Contains(h.view(), "Call Sam") {
		t.Fatalf("the todo I just created is not in the list:\n%s", h.view())
	}
	if got := h.model.todos.selected(); got == nil || got.Title != "Call Sam" {
		t.Errorf("cursor is on %+v, want the new todo", got)
	}
	if !strings.Contains(h.model.toastText(), "Call Sam") {
		t.Errorf("toast = %q, want a receipt naming the todo", h.model.toastText())
	}
}

func TestCaptureRevealsItselfWhenAFilterWouldHideIt(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	// Narrow to Overdue, which cannot contain an undated todo.
	h.press("f")
	if h.model.todos.filter.mode != viewStatus {
		t.Fatalf("mode = %v, want a status filter", h.model.todos.filter.mode)
	}
	narrow := h.model.todos.filter.scopeLabel()

	h.press("N")
	h.press("C", "a", "l", "l", " ", "S", "a", "m", "ctrl+s")

	// The result must be on screen, in a view that shows it, and the
	// message must admit that the view changed.
	if !strings.Contains(h.view(), "Call Sam") {
		t.Fatalf("still hidden after creating it:\n%s", h.view())
	}
	if h.model.todos.filter.mode != viewEverything {
		t.Errorf("mode = %v, want the view opened up", h.model.todos.filter.mode)
	}
	toast := strings.ToLower(h.model.toastText())
	if !strings.Contains(toast, "call sam") {
		t.Errorf("toast = %q, want the receipt", h.model.toastText())
	}
	if !strings.Contains(toast, strings.ToLower(narrow)) {
		t.Errorf("toast = %q, want it to say the view was %s", h.model.toastText(), narrow)
	}
}

func TestSectionsAlwaysIncludeTheInbox(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("N")
	h.press("U", "n", "d", "a", "t", "e", "d", " ", "c", "a", "p", "t", "u", "r", "e", "ctrl+s")
	h.mustContain("Inbox")
	// The inbox heading must carry its count, so its weight is obvious.
	if !strings.Contains(h.view(), "Inbox 2") {
		t.Errorf("inbox heading should show a count:\n%s", h.view())
	}
}

func TestSectionOrderPutsTheMostPressingFirst(t *testing.T) {
	f := sampleAPI()
	now := time.Now()
	past := now.AddDate(0, 0, -3)
	f.todos = []api.Todo{
		{ID: "later", ListID: "l1", Title: "Later thing", ScheduledDay: dayStr(14), CreatedAt: now, UpdatedAt: now},
		{ID: "inbox", ListID: "l1", Title: "Undated thing", CreatedAt: now, UpdatedAt: now},
		{ID: "today", ListID: "l1", Title: "Today thing", ScheduledDay: dayStr(0), CreatedAt: now, UpdatedAt: now},
		{ID: "over", ListID: "l1", Title: "Late thing", DueAt: &past, CreatedAt: now, UpdatedAt: now},
		{ID: "done", ListID: "l1", Title: "Finished thing", IsCompleted: true, CreatedAt: now, UpdatedAt: now},
	}
	h := newHarness(t, f)
	view := h.view()
	order := []string{"Overdue", "Today", "Inbox", "Later", "Done"}
	last := -1
	for _, s := range order {
		i := strings.Index(view, s)
		if i < 0 {
			t.Fatalf("section %q missing:\n%s", s, view)
		}
		if i < last {
			t.Errorf("section %q is out of order:\n%s", s, view)
		}
		last = i
	}
}

func TestCursorNeverLandsOnASectionHeading(t *testing.T) {
	h := newHarness(t, sampleAPI())
	// Walk down far enough to cross every heading, and check we always end
	// on a real todo.
	for i := 0; i < 10; i++ {
		h.press("j")
		if sel := h.model.todos.selected(); sel == nil {
			t.Fatalf("step %d: cursor is on a heading, not a todo", i)
		}
	}
	for i := 0; i < 20; i++ {
		h.press("k")
		if sel := h.model.todos.selected(); sel == nil {
			t.Fatalf("step %d: cursor is on a heading, not a todo", i)
		}
	}
}

func TestTopAndBottomLandOnTodos(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("G")
	if sel := h.model.todos.selected(); sel == nil {
		t.Error("G should land on a todo")
	}
	h.press("g")
	if sel := h.model.todos.selected(); sel == nil {
		t.Error("g should land on a todo")
	}
}

func TestEmptyEverythingSaysSomethingUseful(t *testing.T) {
	f := sampleAPI()
	f.todos = nil
	h := newHarness(t, f)
	h.mustContain("Every todo is done")
	h.mustContain("n adds one")
}

func TestEmptyNarrowedViewPointsBackToEverything(t *testing.T) {
	f := sampleAPI()
	f.todos = nil
	h := newHarness(t, f)
	h.press("f") // Overdue only
	h.mustContain("No overdue todos")
	h.mustContain("f shows everything")
}

func TestNarrowedViewSaysItIsNarrow(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("f")
	h.mustContain("Overdue only")
	h.mustContain("f shows everything")
}

// The whole account is read with a sync, which is not cut at a page size, so
// a large account is not told it is looking at the first of many.
func TestAWholeAccountReadBySyncIsNotCutAtALimit(t *testing.T) {
	f := sampleAPI()
	for i := 0; i < 200; i++ {
		f.todos = append(f.todos, api.Todo{
			ID: fmt.Sprintf("x%d", i), ListID: "l1",
			Title: fmt.Sprintf("Filler %d", i), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		})
	}
	h := newHarness(t, f)
	h.mustContain("202 open")
	h.mustNotContain("first 200")
	if h.model.todos.moreToLoad() {
		t.Error("a full sync was not cut short")
	}
}

func dayStr(offset int) *string {
	s := time.Now().AddDate(0, 0, offset).Format("2006-01-02")
	return &s
}

func TestSectionsAreSortedWithinThemselves(t *testing.T) {
	f := sampleAPI()
	f.todos = []api.Todo{
		{ID: "b", ListID: "l1", Title: "B later", ScheduledDay: dayStr(9), CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "a", ListID: "l1", Title: "A sooner", ScheduledDay: dayStr(5), CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "c", ListID: "l1", Title: "C soonest", ScheduledDay: dayStr(4), CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	h := newHarness(t, f)
	view := h.view()
	iC := strings.Index(view, "C soonest")
	iA := strings.Index(view, "A sooner")
	iB := strings.Index(view, "B later")
	if iC < 0 || iA < 0 || iB < 0 {
		t.Fatalf("missing rows:\n%s", view)
	}
	if iC >= iA || iA >= iB {
		t.Errorf("Later section is not sorted soonest-first:\n%s", view)
	}
}

func TestOverduePutsTheLongestWaitedFirst(t *testing.T) {
	f := sampleAPI()
	now := time.Now()
	long := now.AddDate(0, 0, -9)
	short := now.AddDate(0, 0, -1)
	f.todos = []api.Todo{
		{ID: "short", ListID: "l1", Title: "Missed yesterday", DueAt: &short, CreatedAt: now, UpdatedAt: now},
		{ID: "long", ListID: "l1", Title: "Missed last week", DueAt: &long, CreatedAt: now, UpdatedAt: now},
	}
	h := newHarness(t, f)
	view := h.view()
	if strings.Index(view, "Missed last week") > strings.Index(view, "Missed yesterday") {
		t.Errorf("the longest-waited overdue item should lead:\n%s", view)
	}
}

func TestDoneSectionShowsTheMostRecent(t *testing.T) {
	f := sampleAPI()
	now := time.Now()
	mk := func(id, title string, ago time.Duration) api.Todo {
		at := now.Add(-ago)
		return api.Todo{
			ID: id, ListID: "l1", Title: title, IsCompleted: true,
			CompletedAt: &at, CreatedAt: at, UpdatedAt: at,
		}
	}
	f.todos = []api.Todo{
		mk("old", "Finished ages ago", 72*time.Hour),
		mk("new", "Finished just now", time.Minute),
		mk("mid", "Finished yesterday", 24*time.Hour),
	}
	h := newHarness(t, f)
	view := h.view()
	if strings.Index(view, "Finished just now") > strings.Index(view, "Finished yesterday") {
		t.Errorf("the newest completion should lead the Done section:\n%s", view)
	}
}

func TestCountsAreNotDoubledUp(t *testing.T) {
	h := newHarness(t, sampleAPI())
	// "7 7 todos" is the kind of thing that only shows up on screen.
	view := h.view()
	if strings.Contains(view, "todos  ") || strings.Contains(view, " 3 3") {
		t.Errorf("count rendered twice:\n%s", view)
	}
	if !strings.Contains(h.view(), "3 todos") {
		t.Errorf("expected a plain count:\n%s", h.view())
	}
}

// A rejected token must land on the token prompt, not close the program.
func TestRejectedTokenReturnsToLogin(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.send(todosMsg{err: &api.Error{StatusCode: http.StatusUnauthorized, Code: api.CodeUnauthorized}})
	if h.model.quited {
		t.Fatal("a rejected token should not quit the program")
	}
	if h.model.layer != layerLogin {
		t.Fatalf("layer = %v, want login", h.model.layer)
	}
	if h.model.cfg.Token != "" {
		t.Error("the rejected token should be forgotten")
	}
	h.mustContain("rejected")

	// A second rejection from a request that was already in flight must not
	// reset what the user is typing.
	h.press("e", "v", "t")
	h.send(meMsg{err: &api.Error{StatusCode: http.StatusUnauthorized, Code: api.CodeUnauthorized}})
	if got := h.model.login.tokenInput.Value(); got != "evt" {
		t.Errorf("token input = %q, want %q", got, "evt")
	}
}

// A rejected token that cannot be taken out of the config file would be
// tried again on the next start, so the sign-in screen says it is still
// there.
func TestRejectedTokenThatStaysSavedIsReported(t *testing.T) {
	h := newHarness(t, sampleAPI())
	// A file where the config's directory should be makes every save fail.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVADO_CONFIG", filepath.Join(blocker, "config.json"))
	h.send(todosMsg{err: &api.Error{StatusCode: http.StatusUnauthorized, Code: api.CodeUnauthorized}})
	if h.model.layer != layerLogin {
		t.Fatalf("layer = %v, want login", h.model.layer)
	}
	if !strings.Contains(h.model.login.err, "rejected token is still in it") {
		t.Errorf("login error = %q, want word that the token is still saved", h.model.login.err)
	}
}

// A read of the account that was sent before the timezone changed, and
// answers after the change was saved, must not put the old timezone back.
func TestSlowAccountReadDoesNotUndoTimezoneChange(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	slow := h.model.fetchMe()()
	h.run(h.model.saveTimezone("Europe/Paris"))
	if h.model.user.TZ != "Europe/Paris" {
		t.Fatalf("tz = %q after saving", h.model.user.TZ)
	}
	h.send(slow)
	if h.model.user.TZ != "Europe/Paris" || h.model.loc.String() != "Europe/Paris" {
		t.Errorf("a slower read put back tz %q, loc %s", h.model.user.TZ, h.model.loc)
	}

	// A read sent while the change was on its way may have looked before
	// it landed, whichever of the two answers first.
	for _, readFirst := range []bool{true, false} {
		save := h.model.saveTimezone("Asia/Tokyo")
		read := h.model.fetchMe()
		f.tz = "UTC"
		before := read()
		saved := save()
		if readFirst {
			h.send(before)
			h.send(saved)
		} else {
			h.send(saved)
			h.send(before)
		}
		if h.model.user.TZ != "Asia/Tokyo" {
			t.Errorf("read first %v: tz = %q, want the saved one", readFirst, h.model.user.TZ)
		}
	}
}

// An answer about the account that arrives after signing out belongs to
// the account that left, and must not show it on the sign-in screen.
func TestAccountReadFromBeforeSignOutIsIgnored(t *testing.T) {
	h, _ := signOutHarness(t)
	late := h.model.fetchMe()()
	h.press("4")
	h.model.acct.selectRow("signout")
	h.press("enter", "enter")
	if h.model.layer != layerLogin {
		t.Fatalf("layer = %v, want the sign-in screen", h.model.layer)
	}
	h.send(late)
	if h.model.user.ID != "" || h.model.ready || len(h.model.lists) != 0 {
		t.Errorf("the signed-out account came back: user %q, ready %v, %d lists",
			h.model.user.ID, h.model.ready, len(h.model.lists))
	}
}

// The clock has to keep running while nothing is loading, or a toast that
// arrives after the last request never goes away.
func TestIdleTickKeepsRunning(t *testing.T) {
	h := newHarness(t, sampleAPI())
	if h.model.busy {
		t.Fatal("expected an idle model")
	}
	h.send(toastMsg{kind: toastOK, text: "hello"})
	for i := 0; i < 3; i++ {
		_, cmd := h.model.Update(tickMsg(time.Now()))
		if cmd == nil {
			t.Fatalf("tick %d did not reschedule while idle", i)
		}
	}
	if h.model.toast != nil {
		t.Error("the toast should have expired while idle")
	}
}

// The view the user left open is the view they get back.
func TestSavedViewIsRestored(t *testing.T) {
	m := New(context.Background(), config.Config{
		Token: "evt_test", Mode: "Overdue only", Status: api.StatusOverdue, ListID: "l1", NoCache: true,
	})
	if m.todos.filter.mode != viewStatus || m.todos.filter.status != api.StatusOverdue {
		t.Errorf("mode/status = %v/%q, want status/overdue", m.todos.filter.mode, m.todos.filter.status)
	}
	if m.todos.filter.listID != "l1" {
		t.Errorf("listID = %q, want l1", m.todos.filter.listID)
	}

	m = New(context.Background(), config.Config{Token: "evt_test", Mode: "Everything", NoCache: true})
	if m.todos.filter.mode != viewEverything {
		t.Errorf("mode = %v, want everything", m.todos.filter.mode)
	}
}

func TestViewChoiceIsPersisted(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.press("f") // Everything -> Overdue only
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "Overdue only" || cfg.Status != api.StatusOverdue {
		t.Errorf("saved %q/%q, want Overdue only/overdue", cfg.Mode, cfg.Status)
	}
}

// fakeCursor is a cursor in the shape the service uses: digits, meaning
// nothing to the client. Here they are the milliseconds of now.
func fakeCursor() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }
