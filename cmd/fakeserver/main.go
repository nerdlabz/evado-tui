// Command fakeserver serves a small stand-in for the Evado API so the TUI
// can be driven by hand without an account. It answers only requests that
// carry its token, evt_dev unless -token says otherwise, and listens only
// on loopback unless -remote is given.
//
//	go run ./cmd/fakeserver -addr 127.0.0.1:8787
//	EVADO_CONFIG=/tmp/evado-dev.json go run ./cmd/evado -server http://127.0.0.1:8787/api/v1 -token evt_dev
package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nerdlabz/evado-tui/internal/local"
)

type item struct {
	ID          string     `json:"id"`
	TodoID      string     `json:"todoId"`
	Title       string     `json:"title"`
	Position    int        `json:"position"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type todo struct {
	ID             string     `json:"id"`
	ListID         string     `json:"listId"`
	AssigneeID     *string    `json:"assigneeId"`
	Title          string     `json:"title"`
	RawInput       *string    `json:"rawInput,omitempty"`
	Notes          *string    `json:"notes,omitempty"`
	DoAt           *time.Time `json:"doAt,omitempty"`
	ScheduledDay   *string    `json:"scheduledDay,omitempty"`
	DueAt          *time.Time `json:"dueAt,omitempty"`
	HasTime        bool       `json:"hasTime"`
	RemindAt       *time.Time `json:"remindAt,omitempty"`
	RecurrenceRule *string    `json:"recurrenceRule,omitempty"`
	SortOrder      int        `json:"sortOrder"`
	IsCompleted    bool       `json:"isCompleted"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	DeletedAt      *time.Time `json:"deletedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type member struct {
	UserID   string  `json:"userId"`
	Username *string `json:"username"`
	Email    string  `json:"email"`
	Role     string  `json:"role"`
}

type list struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	IsPersonal bool       `json:"isPersonal"`
	OwnerID    string     `json:"ownerId"`
	Role       string     `json:"role"`
	ReadOnly   bool       `json:"readOnly"`
	DeletesAt  *time.Time `json:"deletesAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	Members    []member   `json:"members,omitempty"`
}

type store struct {
	// mu is held for the whole of each request; see guard.
	mu    sync.Mutex
	todos []*todo
	// tombs are the todos deleted through sync, kept so a pull can say so.
	tombs []*todo
	lists []list
	steps map[string][]*item
	seq   int
	// username is the account's, null until chosen.
	username *string
	// invites are waiting for an answer, declined the ones turned down.
	invites, declined []invitation
}

// invitation is a pending invitation, in the shape the API sends.
type invitation struct {
	ID        string            `json:"id"`
	List      map[string]string `json:"list"`
	InvitedBy map[string]any    `json:"invitedBy"`
	CreatedAt time.Time         `json:"createdAt"`
}

var day = func(offset int) *string {
	d := time.Now().AddDate(0, 0, offset).Format("2006-01-02")
	return &d
}

func at(offsetDays int, hour, min int) *time.Time {
	t := time.Now().AddDate(0, 0, offsetDays)
	t = time.Date(t.Year(), t.Month(), t.Day(), hour, min, 0, 0, time.UTC)
	return &t
}

func newStore() *store {
	now := time.Now()
	l1 := list{
		ID: "l_personal", Name: "Personal", IsPersonal: true, OwnerID: "u1", Role: "owner", CreatedAt: now,
		Members: []member{{UserID: "u1", Email: "you@example.com", Role: "owner"}},
	}
	l2 := list{
		ID: "l_home", Name: "Household", OwnerID: "u1", Role: "owner", CreatedAt: now,
		Members: []member{
			{UserID: "u1", Email: "you@example.com", Role: "owner"},
			{UserID: "u2", Email: "sam@example.com", Role: "member"},
		},
	}
	l3 := list{ID: "l_work", Name: "Work", OwnerID: "u9", Role: "member", ReadOnly: true, CreatedAt: now}

	notes := "meter is in the cupboard by the door"
	s := &store{
		steps: map[string][]*item{},
		lists: []list{l1, l2, l3},
		invites: []invitation{{
			ID: "inv_club", List: map[string]string{"id": "l_club", "name": "Book club"},
			InvitedBy: map[string]any{"userId": "u2", "username": "sam", "email": "s***@example.com"},
			CreatedAt: now.Add(-26 * time.Hour),
		}},
		todos: []*todo{
			{
				ID: "t1", ListID: "l_personal", Title: "Pay the cable bill", Notes: &notes,
				ScheduledDay: day(0), DoAt: at(0, 17, 0), HasTime: true, RemindAt: at(0, 16, 30),
				CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "t2", ListID: "l_home", Title: "Renew the passport", AssigneeID: strPtr("u1"),
				ScheduledDay: day(3), DueAt: at(10, 12, 0), CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "t3", ListID: "l_personal", Title: "Buy oat milk and coffee",
				CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "t4", ListID: "l_personal", Title: "Dentist appointment", ScheduledDay: day(-2),
				DoAt: at(-2, 9, 30), HasTime: true, DueAt: at(-1, 18, 0), CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "t5", ListID: "l_home", Title: "Water the plants", RecurrenceRule: strPtr("1:week:3"),
				ScheduledDay: day(1), CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "t6", ListID: "l_personal", Title: "Book the flights", IsCompleted: true,
				CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "t7", ListID: "l_work", Title: "Review the quarterly plan", ScheduledDay: day(2),
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	s.steps["t1"] = []*item{
		{ID: "c1", TodoID: "t1", Title: "Read the meter", CreatedAt: now},
		{ID: "c2", TodoID: "t1", Title: "Call the provider", CreatedAt: now},
		{
			ID: "c3", TodoID: "t1", Title: "Set a reminder for the direct debit", CreatedAt: now,
			CompletedAt: &now,
		},
	}
	return s
}

func strPtr(s string) *string { return &s }

func (s *store) next(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s%d", prefix, 100+s.seq)
}

func (s *store) write(w http.ResponseWriter, body any) {
	_ = json.NewEncoder(w).Encode(body)
}

func (s *store) fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	s.write(w, map[string]any{"error": msg, "code": code})
}

func (s *store) find(id string) *todo {
	for _, t := range s.todos {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (s *store) findList(id string) int {
	for i, l := range s.lists {
		if l.ID == id {
			return i
		}
	}
	return -1
}

const prefix = "/api/v1"

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address, loopback unless -remote is given")
	token := flag.String("token", "evt_dev", "the bearer token every request must carry")
	remote := flag.Bool("remote", false, "allow listening on an address other machines can reach")
	flag.Parse()

	if err := checkAddr(*addr, *remote); err != nil {
		log.Fatal(err)
	}
	if *token == "" {
		log.Fatal("-token must not be empty")
	}

	// A dev server may hand back a code in the response; expose it so the
	// TUI's behaviour can be exercised without a mailbox.
	if os.Getenv("FAKE_DEV_CODE") != "" {
		log.Printf("dev code: %s", os.Getenv("FAKE_DEV_CODE"))
	}

	log.Printf("fake Evado API listening on http://%s%s", *addr, prefix)
	log.Printf("use: EVADO_CONFIG=/tmp/evado-dev.json go run ./cmd/evado -server http://%s%s -token %s", *addr, prefix, *token)
	if err := http.ListenAndServe(*addr, newServer(newStore(), *token)); err != nil {
		log.Fatal(err)
	}
}

// checkAddr refuses an address other machines could reach, unless asked
// for: the fake account takes any change from whoever holds the token, and
// the token is a default anyone can read here.
func checkAddr(addr string, remote bool) error {
	if remote {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-addr %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("-addr %q is not a loopback address; use 127.0.0.1, or add -remote to listen on it anyway", addr)
}

// newServer serves the fake API for one account, reached with token.
func newServer(s *store, token string) http.Handler {
	mux := http.NewServeMux()

	tz := "Europe/Madrid"
	user := func() map[string]any {
		return map[string]any{
			"id": "u1", "email": "you@example.com", "username": s.username, "tz": tz,
			"createdAt": time.Now().AddDate(-1, 0, 0),
		}
	}
	mux.HandleFunc(prefix+"/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var body struct {
				TZ       *string         `json:"tz"`
				Username json.RawMessage `json:"username"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Username != nil {
				var name *string
				_ = json.Unmarshal(body.Username, &name)
				if name != nil {
					clean := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(*name), "@"))
					if len(clean) < 3 || len(clean) > 20 {
						s.fail(w, http.StatusBadRequest, "username_invalid", "Usernames are 3 to 20 characters")
						return
					}
					if clean == "sam" {
						s.fail(w, http.StatusConflict, "username_taken", "That username is taken")
						return
					}
					name = &clean
				}
				s.username = name
			}
			if body.TZ != nil {
				tz = *body.TZ
			}
			s.write(w, map[string]any{"user": user()})
			return
		}
		s.write(w, map[string]any{"user": user(), "lists": s.lists})
	})

	mux.HandleFunc(prefix+"/people", func(w http.ResponseWriter, r *http.Request) {
		sam := "sam"
		s.write(w, map[string]any{"people": []map[string]any{
			{"userId": "u2", "username": &sam, "email": "s***@example.com", "sharedListIds": []string{"l_home"}},
		}})
	})

	mux.HandleFunc(prefix+"/todos", func(w http.ResponseWriter, r *http.Request) {
		// The real service splits a multi-line title into several todos;
		// do the same so quick capture can be tried out.
		if r.Method == http.MethodPost {
			var body struct {
				ID     string  `json:"id"`
				Title  string  `json:"title"`
				Notes  *string `json:"notes"`
				ListID string  `json:"listId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.ID != "" {
				if existing := s.find(body.ID); existing != nil {
					s.write(w, map[string]any{"todo": existing, "idempotent": true})
					return
				}
			}
			var made []*todo
			for _, line := range strings.Split(body.Title, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				now := time.Now()
				t := &todo{
					ID: firstNonEmpty(body.ID, s.next("t")), ListID: firstNonEmpty(body.ListID, "l_personal"),
					Title: line, Notes: body.Notes, RawInput: &line,
					CreatedAt: now, UpdatedAt: now,
				}
				parseDate(t, line)
				s.todos = append([]*todo{t}, s.todos...)
				made = append(made, t)
			}
			w.WriteHeader(http.StatusCreated)
			s.write(w, map[string]any{"todos": made})
			return
		}
		if since := r.URL.Query().Get("since"); since != "" {
			at, ok := cursorTime(since)
			if !ok {
				s.fail(w, http.StatusBadRequest, "invalid_request", "since must be a cursor the service gave")
				return
			}
			s.write(w, map[string]any{
				"todos":   s.changed(&at),
				"lists":   s.lists,
				"cursor":  newCursor(),
				"hasMore": false,
			})
			return
		}
		q := strings.ToLower(r.URL.Query().Get("q"))
		listID := r.URL.Query().Get("listId")
		undated := r.URL.Query().Get("undated") == "1"
		status := r.URL.Query().Get("status")
		out := []*todo{}
		for _, t := range s.todos {
			if t.DeletedAt != nil {
				continue
			}
			if listID != "" && t.ListID != listID {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(t.Title+" "+deref(t.Notes)), q) {
				continue
			}
			if undated && t.ScheduledDay != nil {
				continue
			}
			// Status filters follow the real service, including the rule
			// that an undated todo is never "today", without that, the
			// client looks correct when it is not.
			today := time.Now().Format("2006-01-02")
			dated := t.ScheduledDay != nil
			isToday := dated && *t.ScheduledDay == today
			keep := true
			switch status {
			case "", "open":
				keep = !t.IsCompleted
			case "completed":
				keep = t.IsCompleted
			case "today":
				keep = isToday
			case "overdue":
				keep = t.DueAt != nil && t.DueAt.Before(time.Now())
			case "upcoming":
				keep = dated && *t.ScheduledDay > today
			case "due":
				keep = t.DueAt != nil
			}
			if !keep {
				continue
			}
			out = append(out, t)
		}
		s.write(w, map[string]any{"todos": out})
	})

	mux.HandleFunc(prefix+"/todos/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/todos/"), "/")
		id, action := parts[0], ""
		if len(parts) > 1 {
			action = parts[1]
		}
		t := s.find(id)
		if t == nil {
			s.fail(w, http.StatusNotFound, "not_found", "No such todo")
			return
		}
		switch action {
		case "checklist":
			if r.Method == http.MethodPost {
				var body struct{ Title string }
				_ = json.NewDecoder(r.Body).Decode(&body)
				it := &item{ID: s.next("c"), TodoID: id, Title: body.Title, Position: len(s.steps[id]), CreatedAt: time.Now()}
				s.steps[id] = append(s.steps[id], it)
				w.WriteHeader(http.StatusCreated)
				s.write(w, map[string]any{"item": it})
				return
			}
			s.write(w, map[string]any{"items": s.steps[id]})
		case "complete":
			now := time.Now()
			t.IsCompleted, t.CompletedAt, t.UpdatedAt = true, &now, now
			var next *todo
			if t.RecurrenceRule != nil {
				next = s.recur(t)
			}
			s.write(w, map[string]any{"todo": t, "next": next})
		case "uncomplete":
			t.IsCompleted, t.CompletedAt, t.UpdatedAt = false, nil, time.Now()
			s.write(w, map[string]any{"todo": t})
		default:
			switch r.Method {
			case http.MethodGet:
				s.write(w, map[string]any{"todo": t})
			case http.MethodPatch:
				var patch map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&patch)
				applyPatch(t, patch)
				t.UpdatedAt = time.Now()
				s.write(w, map[string]any{"todo": t, "next": nil})
			case http.MethodDelete:
				now := time.Now()
				t.DeletedAt, t.UpdatedAt = &now, now
				s.write(w, map[string]any{"ok": true})
			}
		}
	})

	mux.HandleFunc(prefix+"/checklist/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, prefix+"/checklist/")
		for _, items := range s.steps {
			for _, it := range items {
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
						it.Title = *body.Title
					}
					it.CompletedAt = body.CompletedAt
					s.write(w, map[string]any{"item": it})
				case http.MethodDelete:
					kept := items[:0]
					for _, other := range items {
						if other.ID != id {
							kept = append(kept, other)
						}
					}
					s.steps[it.TodoID] = kept
					s.write(w, map[string]any{"ok": true})
				}
				return
			}
		}
		s.fail(w, http.StatusNotFound, "not_found", "No such step")
	})

	mux.HandleFunc(prefix+"/lists", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.lists = append(s.lists, list{
				ID: s.next("l"), Name: body.Name, OwnerID: "u1", Role: "owner", CreatedAt: time.Now(),
			})
		}
		s.write(w, map[string]any{"lists": s.lists})
	})

	mux.HandleFunc(prefix+"/lists/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, prefix+"/lists/")
		parts := strings.Split(rest, "/")
		idx := s.findList(parts[0])
		if idx < 0 {
			s.fail(w, http.StatusNotFound, "not_found", "No such list")
			return
		}
		switch {
		case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodGet:
			s.write(w, map[string]any{"members": s.lists[idx].Members})
		case len(parts) == 2 && parts[1] == "invitations" && r.Method == http.MethodPost:
			// Nobody joins until they accept, so the members do not change.
			w.WriteHeader(http.StatusCreated)
			s.write(w, map[string]any{"ok": true, "invitee": map[string]any{"userId": "u9", "username": nil}})
		case len(parts) == 2 && parts[1] == "invites":
			w.WriteHeader(http.StatusCreated)
			s.write(w, map[string]any{
				"inviteUrl": "https://evado.app/join/" + s.next("i"),
				"expiresIn": 604800,
			})
		case len(parts) == 1 && r.Method == http.MethodPatch:
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.lists[idx].Name = body.Name
			s.write(w, map[string]any{"list": s.lists[idx]})
		case len(parts) == 1 && r.Method == http.MethodDelete:
			if s.lists[idx].IsPersonal {
				s.fail(w, http.StatusBadRequest, "bad_request", "The personal list cannot be deleted")
				return
			}
			s.lists = append(s.lists[:idx], s.lists[idx+1:]...)
			s.write(w, map[string]any{"ok": true})
		default:
			s.write(w, map[string]any{"ok": true})
		}
	})

	mux.HandleFunc(prefix+"/join", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Code string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(body.Code, "i") {
			s.fail(w, http.StatusNotFound, "not_found", "Invalid or expired invite")
			return
		}
		s.write(w, map[string]any{"ok": true})
	})

	mux.HandleFunc(prefix+"/parse", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Title string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		p := local.Parse(body.Title, time.Local, time.Now())
		s.write(w, map[string]any{
			"parsed": map[string]any{
				"title": p.Title, "doAt": p.DoAt, "hasTime": p.HasTime,
				"recurrenceRule": p.Recurrence, "assumedTime": p.Assumed,
			},
		})
	})

	mux.HandleFunc(prefix+"/sync", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Since   string                       `json:"since"`
			Changes []map[string]json.RawMessage `json:"changes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var since *time.Time
		if body.Since != "" {
			at, ok := cursorTime(body.Since)
			if !ok {
				s.fail(w, http.StatusBadRequest, "invalid_request", "since must be a cursor the service gave")
				return
			}
			since = &at
		}
		for _, ch := range body.Changes {
			s.apply(ch)
		}
		s.write(w, map[string]any{
			"todos":       s.changed(since),
			"lists":       s.lists,
			"invitations": s.invites,
			"cursor":      newCursor(),
			"hasMore":     false,
			"errors":      []any{},
		})
	})

	// Invitations: one is waiting from the start, so the inbox has
	// something to accept, decline and restore.
	mux.HandleFunc(prefix+"/invitations", func(w http.ResponseWriter, r *http.Request) {
		s.write(w, map[string]any{"invitations": s.invites})
	})
	mux.HandleFunc(prefix+"/invitations/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/invitations/"), "/")
		if len(parts) != 2 || r.Method != http.MethodPost {
			s.fail(w, http.StatusNotFound, "not_found", "Not found")
			return
		}
		from, to := &s.invites, &s.declined
		if parts[1] == "restore" {
			from, to = &s.declined, &s.invites
		}
		for i, inv := range *from {
			if inv.ID != parts[0] {
				continue
			}
			*from = append((*from)[:i:i], (*from)[i+1:]...)
			switch parts[1] {
			case "accept":
				joined := list{
					ID: inv.List["id"], Name: inv.List["name"], Role: "member", CreatedAt: time.Now(),
					Members: []member{{UserID: "u1", Email: "you@example.com", Role: "member"}},
				}
				s.lists = append(s.lists, joined)
				s.write(w, map[string]any{"list": joined})
			case "decline":
				*to = append(*to, inv)
				s.write(w, map[string]any{"ok": true})
			case "restore":
				*to = append([]invitation{inv}, *to...)
				s.write(w, map[string]any{"invitation": inv})
			default:
				s.fail(w, http.StatusNotFound, "not_found", "Not found")
			}
			return
		}
		s.fail(w, http.StatusNotFound, "not_found", "Not found")
	})

	// These reject token auth on the real service; mirror that so the
	// client's explanations are exercised.
	sessionOnly := func(msg string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s.fail(w, http.StatusForbidden, "session_required", msg)
		}
	}
	mux.HandleFunc(prefix+"/tokens", sessionOnly("Token management needs a browser session"))
	mux.HandleFunc(prefix+"/tokens/", sessionOnly("Token management needs a browser session"))
	mux.HandleFunc(prefix+"/notification-channels", sessionOnly("Channel management needs a browser session"))
	mux.HandleFunc(prefix+"/notification-channels/", sessionOnly("Channel management needs a browser session"))
	mux.HandleFunc(prefix+"/me/export", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, http.StatusForbidden, "forbidden", "Export needs the account scope, which pasted tokens lack")
	})

	return s.guard(token, mux)
}

// guard turns away a request without the bearer token, as the service does,
// and lets one request in at a time. The handlers read and change the store
// freely, and responses are encoded from it, so the whole request holds the
// lock: this is a stand-in for one person, not something that has to scale.
func (s *store) guard(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			s.fail(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid token")
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func applyPatch(t *todo, patch map[string]json.RawMessage) {
	read := func(key string) (string, bool) {
		raw, ok := patch[key]
		if !ok || string(raw) == "null" {
			return "", ok && string(raw) == "null"
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s, true
	}
	if v, ok := read("title"); ok && v != "" {
		t.Title = v
	}
	if v, ok := read("notes"); ok {
		if v == "" {
			t.Notes = nil
		} else {
			t.Notes = &v
		}
	}
	if v, ok := read("listId"); ok && v != "" {
		t.ListID = v
	}
	if v, ok := read("assigneeId"); ok {
		if v == "" {
			t.AssigneeID = nil
		} else {
			t.AssigneeID = &v
		}
	}
	if v, ok := read("scheduledDay"); ok {
		if v == "" {
			t.ScheduledDay, t.DoAt, t.HasTime = nil, nil, false
		} else {
			t.ScheduledDay = &v
			t.DoAt = at(0, 9, 0)
			t.HasTime = false
		}
	}
	if v, ok := read("dueAt"); ok {
		if v == "" {
			t.DueAt = nil
		} else if ts, err := time.Parse(time.RFC3339, v); err == nil {
			t.DueAt = &ts
			t.RemindAt = &ts
		}
	}
	if v, ok := read("remindAt"); ok {
		if v == "" {
			t.RemindAt = nil
		} else if ts, err := time.Parse(time.RFC3339, v); err == nil {
			t.RemindAt = &ts
		}
	}
	if v, ok := read("recurrenceRule"); ok {
		if v == "" {
			t.RecurrenceRule = nil
		} else {
			t.RecurrenceRule = &v
		}
	}
	if raw, ok := patch["isCompleted"]; ok && string(raw) != "null" {
		_ = json.Unmarshal(raw, &t.IsCompleted)
		if t.IsCompleted {
			now := time.Now()
			t.CompletedAt = &now
		} else {
			t.CompletedAt = nil
		}
	}
}

// parseDate is a rough stand-in for the server's natural-language parser,
// enough to make dates show up in the UI.
func parseDate(t *todo, text string) {
	lower := strings.ToLower(text)
	now := time.Now()
	offset := 0
	matched := false
	switch {
	case strings.Contains(lower, "tomorrow"):
		offset, matched = 1, true
	case strings.Contains(lower, "today"):
		matched = true
	case strings.Contains(lower, "monday"):
		offset, matched = int(8-now.Weekday())%7, true
	case strings.Contains(lower, "friday"):
		offset, matched = int(5-now.Weekday()+7)%7, true
	case strings.Contains(lower, "next week"):
		offset, matched = 7, true
	}
	if matched {
		d := now.AddDate(0, 0, offset).Format("2006-01-02")
		t.ScheduledDay = &d
	}
	hour, min := 0, 0
	if idx := strings.Index(lower, " at "); idx >= 0 {
		rest := strings.TrimSpace(text[idx+4:])
		if ts, err := time.Parse("3:04pm", strings.ReplaceAll(rest, " ", "")); err == nil {
			hour, min, t.HasTime = ts.Hour(), ts.Minute(), true
		}
	}
	if t.ScheduledDay != nil {
		day, _ := time.Parse("2006-01-02", *t.ScheduledDay)
		ts := time.Date(day.Year(), day.Month(), day.Day(), hour, min, 0, 0, time.UTC)
		t.DoAt = &ts
	}
}

func (s *store) recur(t *todo) *todo {
	now := time.Now()
	next := *t
	next.ID = s.next("t")
	next.IsCompleted, next.CompletedAt = false, nil
	if t.ScheduledDay != nil {
		d, _ := time.Parse("2006-01-02", *t.ScheduledDay)
		d = d.AddDate(0, 0, 7)
		ds := d.Format("2006-01-02")
		next.ScheduledDay = &ds
		next.DoAt = &now
	}
	next.CreatedAt, next.UpdatedAt = now, now
	s.todos = append([]*todo{&next}, s.todos...)
	return &next
}

// apply takes one pushed change the way the service does: the todo is
// made if it is new, only the fields sent are touched, and a change that
// is not newer than what is stored is skipped.
func (s *store) apply(ch map[string]json.RawMessage) {
	var id string
	_ = json.Unmarshal(ch["id"], &id)
	var updated time.Time
	_ = json.Unmarshal(ch["updatedAt"], &updated)
	t := s.find(id)
	if t != nil && !updated.After(t.UpdatedAt) {
		return
	}
	now := time.Now()
	if t == nil {
		t = &todo{ID: id, ListID: s.lists[0].ID, Title: "Untitled", CreatedAt: now}
		s.todos = append(s.todos, t)
	}
	text := func(key string, dst **string) {
		if raw, ok := ch[key]; ok {
			*dst = nil
			_ = json.Unmarshal(raw, dst)
		}
	}
	when := func(key string, dst **time.Time) {
		if raw, ok := ch[key]; ok {
			*dst = nil
			_ = json.Unmarshal(raw, dst)
		}
	}
	if raw, ok := ch["title"]; ok {
		_ = json.Unmarshal(raw, &t.Title)
	}
	if raw, ok := ch["listId"]; ok {
		_ = json.Unmarshal(raw, &t.ListID)
	}
	text("notes", &t.Notes)
	text("assigneeId", &t.AssigneeID)
	text("recurrenceRule", &t.RecurrenceRule)
	when("dueAt", &t.DueAt)
	when("remindAt", &t.RemindAt)
	if _, ok := ch["doAt"]; ok {
		when("doAt", &t.DoAt)
		t.HasTime, t.ScheduledDay = t.DoAt != nil, nil
		if t.DoAt != nil {
			day := t.DoAt.Local().Format("2006-01-02")
			t.ScheduledDay = &day
		}
	}
	if _, ok := ch["scheduledDay"]; ok {
		text("scheduledDay", &t.ScheduledDay)
		t.DoAt, t.HasTime = nil, false
		if t.ScheduledDay != nil {
			if d, err := time.ParseInLocation("2006-01-02", *t.ScheduledDay, time.Local); err == nil {
				at := d.Add(9 * time.Hour)
				t.DoAt = &at
			}
		}
	}
	if raw, ok := ch["isCompleted"]; ok {
		_ = json.Unmarshal(raw, &t.IsCompleted)
		t.CompletedAt = nil
		if t.IsCompleted {
			t.CompletedAt = &now
		}
	}
	if raw, ok := ch["checklist"]; ok {
		var steps []*item
		_ = json.Unmarshal(raw, &steps)
		for i, st := range steps {
			st.TodoID, st.Position = id, i
		}
		s.steps[id] = steps
	}
	t.UpdatedAt = now
	if string(ch["deleted"]) == "true" {
		// Kept as a tombstone, so other devices hear of the deletion.
		t.DeletedAt = &now
		s.tombs = append(s.tombs, t)
		kept := s.todos[:0]
		for _, x := range s.todos {
			if x.ID != id {
				kept = append(kept, x)
			}
		}
		s.todos = kept
	}
}

// newCursor and cursorTime stand in for the service's change-log positions.
// They are digits, like the real ones, and here mean milliseconds; a client
// must not rely on that.
func newCursor() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }

func cursorTime(c string) (time.Time, bool) {
	ms, err := strconv.ParseInt(c, 10, 64)
	return time.UnixMilli(ms), err == nil && len(c) <= 15
}

// changed is the pull half of a sync: every live todo for a first pull,
// and after that whatever changed since, tombstones included.
func (s *store) changed(since *time.Time) []map[string]any {
	out := []map[string]any{}
	add := func(t *todo) {
		if since != nil && !t.UpdatedAt.After(*since) {
			return
		}
		entry := map[string]any{}
		b, _ := json.Marshal(t)
		_ = json.Unmarshal(b, &entry)
		entry["checklist"] = s.steps[t.ID]
		out = append(out, entry)
	}
	for _, t := range s.todos {
		add(t)
	}
	if since != nil {
		for _, t := range s.tombs {
			add(t)
		}
	}
	return out
}
