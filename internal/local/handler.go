package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// serve answers one API request. Todos, checklists and lists are answered
// from this computer. Everything that only the service can do, such as
// sharing a list, is passed on to it.
func (b *Backend) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	seg := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/local/export" && r.Method == http.MethodGet:
		ex, err := b.ExportChecked()
		if err != nil {
			fail(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := api.EncodeTodosExport(w, ex); err != nil {
			fail(w, http.StatusInternalServerError, "internal", err.Error())
		}
	case path == "/local/import" && r.Method == http.MethodPost:
		ex, err := api.ReadExport(r.Body)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_export", err.Error())
			return
		}
		result, err := b.Import(ex, api.ImportOptions{DryRun: r.URL.Query().Get("dryRun") == "true", ListID: r.URL.Query().Get("listId")})
		if err != nil {
			var apiErr *api.Error
			if errors.As(err, &apiErr) {
				fail(w, apiErr.StatusCode, apiErr.Code, apiErr.Message)
			} else {
				fail(w, http.StatusInternalServerError, "internal", err.Error())
			}
			return
		}
		reply(w, http.StatusOK, result)
	case path == "/me" && r.Method == http.MethodGet:
		b.locked(w, func() (int, any) {
			return http.StatusOK, map[string]any{"user": b.st.User, "lists": b.lists(), "smartParsingAvailable": b.st.User.SmartParsingAvailable}
		})
	case path == "/me" && r.Method == http.MethodPatch:
		b.patchMe(w, r, path)
	case path == "/invitations" && r.Method == http.MethodGet:
		b.invitations(w, r)
	case path == "/parse" && r.Method == http.MethodPost:
		b.parse(w, r)
	case path == "/todos" && r.Method == http.MethodGet:
		b.listTodos(w, r, path)
	case path == "/todos" && r.Method == http.MethodPost:
		b.createTodos(w, r)
	case seg[0] == "todos" && len(seg) >= 2:
		b.todo(w, r, seg[1:])
	case seg[0] == "checklist" && len(seg) == 2:
		b.step(w, r, seg[1])
	case path == "/lists" && r.Method == http.MethodGet:
		b.locked(w, func() (int, any) { return http.StatusOK, map[string]any{"lists": b.lists()} })
	case path == "/lists" && r.Method == http.MethodPost:
		b.createList(w, r, path)
	case seg[0] == "lists" && len(seg) == 2 && (r.Method == http.MethodPatch || r.Method == http.MethodDelete):
		b.changeList(w, r, seg[1])
	case seg[0] == "lists" && len(seg) == 3 && seg[2] == "members" && r.Method == http.MethodGet:
		b.members(w, r, path, seg[1])
	case seg[0] == "lists" && len(seg) == 3 && (seg[2] == "invitations" || seg[2] == "invites") && r.Method == http.MethodPost:
		b.share(w, r, path, seg[1])
	case path == "/sync" && r.Method == http.MethodPost:
		b.fullRead(w, r, path)
	default:
		b.proxy(w, r, path)
	}
}

// -- plumbing ---------------------------------------------------------------

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// fail answers with an error in the shape the service uses.
func fail(w http.ResponseWriter, status int, code, msg string) {
	reply(w, status, map[string]any{"error": msg, "code": code})
}

// problem is an error answer on its way out of a locked section.
type problem struct {
	status    int
	code, msg string
}

func notFound() problem { return problem{http.StatusNotFound, api.CodeNotFound, "not found"} }
func invalid(msg string) problem {
	return problem{http.StatusBadRequest, "invalid_request", msg}
}

func readOnly() problem {
	return problem{http.StatusForbidden, "list_read_only", "this list is read-only"}
}

// locked runs fn holding the lock and sends what it returns. A problem is
// sent as an error; anything else as the body.
func (b *Backend) locked(w http.ResponseWriter, fn func() (int, any)) {
	b.mu.Lock()
	status, body := fn()
	b.mu.Unlock()
	if p, ok := body.(problem); ok {
		fail(w, p.status, p.code, p.msg)
		return
	}
	reply(w, status, body)
}

// change is locked for something that alters the state: it saves the file
// when fn succeeds.
func (b *Backend) change(w http.ResponseWriter, fn func() (int, any)) {
	b.locked(w, func() (int, any) {
		status, body := fn()
		if _, bad := body.(problem); bad {
			return status, body
		}
		if err := b.persist(); err != nil {
			return 0, problem{http.StatusInternalServerError, "internal", "could not save: " + err.Error()}
		}
		return status, body
	})
}

// signedIn reports whether there is an account behind the todos here.
// The caller holds the lock.
func (b *Backend) signedIn() bool { return b.remote != nil && b.st.UserID != "" }

// proxy passes a request on to the service and its answer back.
func (b *Backend) proxy(w http.ResponseWriter, r *http.Request, path string) {
	if !b.forward(w, r, path) {
		fail(w, http.StatusServiceUnavailable, api.CodeOffline, "could not reach Evado; this needs a connection")
	}
}

// forward is proxy that writes nothing when the service cannot be reached,
// and reports whether it was, so the caller can do the work here instead.
func (b *Backend) forward(w http.ResponseWriter, r *http.Request, path string) bool {
	b.mu.Lock()
	remote, link, signed := b.remote, b.link, b.signedIn()
	b.mu.Unlock()
	if !signed {
		fail(w, http.StatusForbidden, "forbidden", "this needs an Evado account; sign in from Account, Connection")
		return true
	}
	url := remote.BaseURL() + path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	// The body is read in full so it can be sent again, which a session
	// does after renewing a token the service turned down.
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			fail(w, http.StatusBadRequest, "bad_request", err.Error())
			return true
		}
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, bytes.NewReader(body))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return true
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "evado-tui")
	web := b.web
	if link.HTTP != nil {
		web = link.HTTP
	} else {
		req.Header.Set("Authorization", "Bearer "+link.Token)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	res, err := web.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := api.ReadResponse(res.Body)
	if err != nil {
		// Passing on part of an answer could read as a whole one.
		fail(w, http.StatusBadGateway, api.CodeUpstreamFailed, "read response: "+err.Error())
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(raw)

	if res.StatusCode >= 300 || r.Method == http.MethodGet {
		return true
	}
	// Something changed on the service that the todos here depend on.
	switch {
	case path == "/me" && r.Method == http.MethodDelete:
		_ = b.Forget()
	case strings.HasPrefix(path, "/invitations"):
		b.syncFresh(r.Context())
	case strings.HasPrefix(path, "/lists") || path == "/join":
		_, _ = b.Sync(r.Context())
	}
	return true
}

// syncFresh syncs once any sync already running has finished. Answering an
// invitation changes what the account can see, which a sync that began
// before it cannot have fetched, and Sync does nothing while one runs.
func (b *Backend) syncFresh(ctx context.Context) {
	for i := 0; i < 100; i++ {
		b.mu.Lock()
		busy := b.syncing
		b.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
	_, _ = b.Sync(ctx)
}

// invitations answers GET /invitations. With a connection the service is
// asked, so an answer is never older than the last action, and what it says
// replaces the copy kept here. Without one the copy kept by the last sync
// is given, marked as such so the interface can say it may be out of date.
func (b *Backend) invitations(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	remote, signed := b.remote, b.signedIn()
	b.mu.Unlock()
	if !signed {
		fail(w, http.StatusForbidden, "forbidden", "invitations need an Evado account; sign in from Account, Connection")
		return
	}
	var live api.InvitationList
	err := remote.Call(r.Context(), http.MethodGet, "/invitations", nil, &live)
	var apiErr *api.Error
	switch {
	case err == nil:
		if live.Invitations == nil {
			live.Invitations = []api.Invitation{}
		}
		b.mu.Lock()
		b.st.Invitations = live.Invitations
		_ = b.persist()
		b.mu.Unlock()
		reply(w, http.StatusOK, live)
	case errors.As(err, &apiErr):
		if apiErr.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(apiErr.RetryAfter.Seconds())))
		}
		fail(w, apiErr.StatusCode, apiErr.Code, apiErr.Message)
	default:
		b.mu.Lock()
		kept := append([]api.Invitation{}, b.st.Invitations...)
		known := b.st.Invitations != nil
		b.mu.Unlock()
		if !known {
			fail(w, http.StatusServiceUnavailable, api.CodeOffline, "could not reach Evado, and no invitations have been saved on this computer yet")
			return
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].CreatedAt.After(kept[j].CreatedAt) })
		reply(w, http.StatusOK, api.InvitationList{Invitations: kept, Cached: true})
	}
}

// lists is the lists in the API's shape. The caller holds the lock.
func (b *Backend) lists() []api.List {
	out := make([]api.List, 0, len(b.st.Lists))
	for _, l := range b.st.Lists {
		out = append(out, l.List)
	}
	return out
}

func (b *Backend) list(id string) *List {
	for i := range b.st.Lists {
		if b.st.Lists[i].ID == id {
			return &b.st.Lists[i]
		}
	}
	return nil
}

func (b *Backend) personal() string {
	for _, l := range b.st.Lists {
		if l.IsPersonal {
			return l.ID
		}
	}
	return b.st.Lists[0].ID
}

// find is the live todo with an id, or nil. The caller holds the lock.
func (b *Backend) find(id string) *Todo {
	for i := range b.st.Todos {
		if b.st.Todos[i].ID == id && b.st.Todos[i].DeletedAt == nil {
			return &b.st.Todos[i]
		}
	}
	return nil
}

// -- account ----------------------------------------------------------------

func (b *Backend) patchMe(w http.ResponseWriter, r *http.Request, path string) {
	b.mu.Lock()
	signed := b.signedIn()
	b.mu.Unlock()
	if signed {
		// The service owns the timezone and the username; what it answers
		// is kept here at once, so labels are right before the next sync.
		rec := &recorder{header: http.Header{}, status: http.StatusOK}
		b.proxy(rec, r, path)
		b.keepProfile(rec)
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
		if rec.status < 300 {
			_, _ = b.Sync(r.Context())
		}
		return
	}
	var body struct {
		TZ       string          `json:"tz"`
		Username json.RawMessage `json:"username"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Username != nil {
		// A username is how other people find an account, so there has to
		// be one to find.
		fail(w, http.StatusForbidden, "forbidden", "a username needs an Evado account; sign in from Account, Connection")
		return
	}
	b.change(w, func() (int, any) {
		if _, err := time.LoadLocation(body.TZ); err != nil || body.TZ == "" {
			return 0, invalid("that is not a timezone; use a name such as Europe/Madrid")
		}
		b.st.User.TZ = body.TZ
		// A date-only todo belongs to a day, not an instant, so its day is
		// read again in the new zone.
		for i := range b.st.Todos {
			b.st.Todos[i].ScheduledDay = b.dayOf(b.st.Todos[i].DoAt)
		}
		return http.StatusOK, map[string]any{"user": b.st.User}
	})
}

// keepProfile takes the account the service returned from a profile change
// into the file, so a new username shows in every label, offline included.
func (b *Backend) keepProfile(rec *recorder) {
	if rec.status >= 300 {
		return
	}
	var out struct {
		User *api.User `json:"user"`
	}
	if json.Unmarshal(rec.body.Bytes(), &out) != nil || out.User == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if out.User.ID != b.st.UserID {
		return
	}
	out.User.SmartParsingAvailable = b.st.User.SmartParsingAvailable
	b.st.User = *out.User
	b.nameSelf()
	_ = b.persist()
}

// nameSelf puts the account's own username on its entry in every list's
// members, which the service only sends again on a later sync. The caller
// holds the lock.
func (b *Backend) nameSelf() {
	for i := range b.st.Lists {
		for j := range b.st.Lists[i].Members {
			if m := &b.st.Lists[i].Members[j]; m.UserID == b.st.User.ID {
				m.Username = b.st.User.Username
			}
		}
	}
}

func (b *Backend) parse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Smart *bool  `json:"smart,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// The preview reads the text the way capturing it would.
	read := b.readings(r.Context(), []string{body.Title}, body.Smart)
	b.locked(w, func() (int, any) {
		p := b.reading(body.Title, read)
		var rule any
		if p.Recurrence != "" {
			rule = p.Recurrence
		}
		return http.StatusOK, map[string]any{"parsed": map[string]any{
			"title": p.Title, "doAt": p.DoAt, "hasTime": p.HasTime, "recurrenceRule": rule,
			"assumedTime": p.Assumed, "keptAsName": p.KeptAsName,
		}}
	})
}

// -- dates ------------------------------------------------------------------

// dayOf is the calendar day of an instant in the account's timezone.
func (b *Backend) dayOf(at *time.Time) *string {
	if at == nil {
		return nil
	}
	day := at.In(b.loc()).Format("2006-01-02")
	return &day
}

// morning is nine o'clock on a day, which is when a todo with a date but
// no time is held to happen, as the service does.
func (b *Backend) morning(day string) (*time.Time, bool) {
	d, err := time.ParseInLocation("2006-01-02", day, b.loc())
	if err != nil {
		return nil, false
	}
	at := time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, b.loc())
	return &at, true
}

var instantLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"}

// instant reads a moment: an ISO timestamp, a local date and time, or
// words such as "friday 5pm".
func (b *Backend) instant(s string) (*time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, true
	}
	for _, l := range instantLayouts {
		if t, err := time.ParseInLocation(l, s, b.loc()); err == nil {
			return &t, true
		}
	}
	if p := Parse(s, b.loc(), b.now()); p.DoAt != nil {
		if !p.HasTime {
			return b.morning(p.DoAt.In(b.loc()).Format("2006-01-02"))
		}
		return p.DoAt, true
	}
	return nil, false
}

// setDo sets when a todo is to be done.
func (b *Backend) setDo(t *Todo, at *time.Time, hasTime bool) {
	t.DoAt, t.HasTime = at, hasTime && at != nil
	t.ScheduledDay = b.dayOf(at)
}

// defaultReminder is when a todo reminds if nobody said: at its time when
// it has one, at nine that morning when it only has a day, and at the
// deadline when that is all there is.
func (b *Backend) defaultReminder(t *Todo) *time.Time {
	switch {
	case t.DoAt != nil && t.HasTime:
		return t.DoAt
	case t.DoAt != nil:
		at, _ := b.morning(t.DoAt.In(b.loc()).Format("2006-01-02"))
		return at
	}
	return t.DueAt
}

// -- todos ------------------------------------------------------------------

// effective is the date a todo is sorted and filtered by: its do date,
// falling back to its deadline.
func effective(t *Todo) *time.Time {
	if t.DoAt != nil {
		return t.DoAt
	}
	return t.DueAt
}

// mentions reports whether the title or notes of a todo hold the lowercase
// needle. The two are searched apart so nothing is joined per todo, which
// only differs from searching them joined with a newline for a needle
// that itself spans the line break.
func mentions(t *Todo, needle string) bool {
	if strings.Contains(strings.ToLower(t.Title), needle) {
		return true
	}
	notes := ""
	if t.Notes != nil {
		notes = *t.Notes
		if strings.Contains(strings.ToLower(notes), needle) {
			return true
		}
	}
	return strings.Contains(needle, "\n") && strings.Contains(strings.ToLower(t.Title+"\n"+notes), needle)
}

func (b *Backend) listTodos(w http.ResponseWriter, r *http.Request, path string) {
	q := r.URL.Query()
	if q.Has("since") {
		b.mu.Lock()
		signed := b.signedIn()
		b.mu.Unlock()
		if signed {
			// The change feed is the service's record of what other
			// devices did, which only it can give.
			b.proxy(w, r, path)
			return
		}
		// With no account there is no change log: deleting a todo the
		// service never had leaves nothing behind, so a feed read from
		// this file could not say what went, and a reader keeping a copy
		// would keep it forever. Refused as the full read refuses it.
		fail(w, http.StatusForbidden, "forbidden", "the todos on this computer have no change log to read; that needs an Evado account")
		return
	}

	var after *pageKey
	if q.Has("cursor") {
		k, ok := parsePageCursor(q.Get("cursor"))
		if !ok {
			fail(w, http.StatusBadRequest, "invalid_request", "expected the nextCursor from a previous page")
			return
		}
		after = &k
	}

	b.locked(w, func() (int, any) {
		now := b.now().In(b.loc())
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, b.loc())
		tomorrow := today.AddDate(0, 0, 1)
		status := q.Get("status")
		if status == "" {
			status = api.StatusOpen
			if q.Get("q") != "" {
				status = api.StatusAll // search spans completed todos too
			}
		}
		var from, to *time.Time
		var done *bool
		yes, no := true, false
		switch status {
		case api.StatusAll:
		case api.StatusCompleted:
			done = &yes
		case api.StatusOverdue:
			done, to = &no, &today
		case api.StatusToday:
			done, from, to = &no, &today, &tomorrow
		case api.StatusDue:
			done, to = &no, &tomorrow
		case api.StatusUpcoming:
			done, from = &no, &tomorrow
		default:
			done = &no
		}
		assigned := q.Get("assigned")
		if assigned == "me" {
			assigned = b.st.User.ID
		}
		needle := strings.ToLower(strings.TrimSpace(q.Get("q")))

		// Only positions are collected and ordered, and the todos are copied
		// once the window is known, so a page of 100 out of a large store
		// does not first copy every match.
		var match []int
		for i := range b.st.Todos {
			t := &b.st.Todos[i]
			eff := effective(t)
			switch {
			case t.DeletedAt != nil,
				q.Get("listId") != "" && t.ListID != q.Get("listId"),
				done != nil && t.IsCompleted != *done,
				from != nil && (eff == nil || eff.Before(*from)),
				to != nil && (eff == nil || !eff.Before(*to)),
				q.Has("undated") && eff != nil,
				assigned == "anyone" && t.AssigneeID != nil,
				assigned != "" && assigned != "anyone" && (t.AssigneeID == nil || *t.AssigneeID != assigned):
				continue
			}
			if needle != "" && !mentions(t, needle) {
				continue
			}
			match = append(match, i)
		}
		// Dated first and soonest first, then the newest capture. The id
		// settles ties, so the order is the same on every read and a cursor
		// can name a place in it.
		sort.Slice(match, func(i, j int) bool {
			return keyOf(&b.st.Todos[match[i]]).before(keyOf(&b.st.Todos[match[j]]))
		})
		if after != nil {
			match = match[sort.Search(len(match), func(i int) bool {
				return after.before(keyOf(&b.st.Todos[match[i]]))
			}):]
		}
		// The store has nothing to protect from a large page, and a view of
		// everything on this computer asks for one, so unlike the service's
		// there is no ceiling on the limit.
		limit := 100
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			limit = n
		}
		var next any
		if len(match) > limit {
			match = match[:limit]
			next = pageCursor(keyOf(&b.st.Todos[match[limit-1]]))
		}
		// The steps go with each todo, so the list can show progress and the
		// preview a checklist without a request per row. Empty is sent as
		// [], which says "no steps" rather than "not told".
		type row struct {
			api.Todo
			Checklist []api.ChecklistItem `json:"checklist"`
		}
		out := make([]row, 0, len(match))
		for _, i := range match {
			t := &b.st.Todos[i]
			steps := t.Steps
			if steps == nil {
				steps = []api.ChecklistItem{}
			}
			out = append(out, row{t.Todo, steps})
		}
		return http.StatusOK, map[string]any{"todos": out, "nextCursor": next}
	})
}

// pageKey is where a todo sits in the order of a listing, to the
// millisecond, which is how finely the service's cursors hold it.
type pageKey struct {
	eff     int64
	dated   bool
	created int64
	id      string
}

func keyOf(t *Todo) pageKey {
	k := pageKey{created: t.CreatedAt.UnixMilli(), id: t.ID}
	if e := effective(t); e != nil {
		k.eff, k.dated = e.UnixMilli(), true
	}
	return k
}

// before reports whether a sorts ahead of c: dated todos first and soonest
// first, then the newest capture, then the greater id.
func (a pageKey) before(c pageKey) bool {
	switch {
	case a.dated != c.dated:
		return a.dated
	case a.eff != c.eff:
		return a.eff < c.eff
	case a.created != c.created:
		return a.created > c.created
	}
	return a.id > c.id
}

// pageCursorShape is the form of the cursor the service hands out for the
// next page: the effective date in milliseconds (empty when there is none),
// the creation time in milliseconds and the id, joined by colons.
var pageCursorShape = regexp.MustCompile(`^(-?\d*):(-?\d+):([\w-]+)$`)

func pageCursor(k pageKey) string {
	eff := ""
	if k.dated {
		eff = strconv.FormatInt(k.eff, 10)
	}
	return eff + ":" + strconv.FormatInt(k.created, 10) + ":" + k.id
}

// parsePageCursor reads a cursor made by pageCursor. It refuses what the
// service would, so a script sees the same answer on both.
func parsePageCursor(s string) (pageKey, bool) {
	m := pageCursorShape.FindStringSubmatch(s)
	if m == nil || len(s) > 120 {
		return pageKey{}, false
	}
	k := pageKey{id: m[3]}
	var err error
	if k.created, err = strconv.ParseInt(m[2], 10, 64); err != nil {
		return pageKey{}, false
	}
	if m[1] != "" {
		if k.eff, err = strconv.ParseInt(m[1], 10, 64); err != nil {
			return pageKey{}, false
		}
		k.dated = true
	}
	return k, true
}

// idPattern is the form the service accepts for an id made on the client.
func validID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF-", c) {
			return false
		}
	}
	return true
}

func (b *Backend) createTodos(w http.ResponseWriter, r *http.Request) {
	var n api.NewTodo
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil || strings.TrimSpace(n.Title) == "" || StoredTitle(n.Title) == "" {
		fail(w, http.StatusBadRequest, "invalid_request", "a todo needs a title")
		return
	}
	if n.Notes != nil && TextUnits(*n.Notes) > 10000 {
		fail(w, http.StatusBadRequest, "invalid_request", "notes are at most 10000 characters")
		return
	}
	// Capture may hold several todos, one per line. The service is asked
	// how it reads them before the lock is taken, since that waits on it.
	lines := captureLines(n)
	if len(lines) > maxCaptureLines {
		// Nothing is made, as on the service, so no line is lost quietly.
		reply(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("%d lines is more than one capture holds (%d)", len(lines), maxCaptureLines),
			"code":  api.CodeTooManyLines, "limit": maxCaptureLines, "lines": len(lines),
		})
		return
	}
	var read map[string]Parsed
	if n.Parse == nil || *n.Parse {
		read = b.readings(r.Context(), lines, n.Smart)
	}
	b.change(w, func() (int, any) {
		listID := n.ListID
		if listID == "" {
			listID = b.personal()
		}
		l := b.list(listID)
		if l == nil {
			return 0, problem{http.StatusNotFound, api.CodeNotFound, "unknown list"}
		}
		if l.ReadOnly {
			return 0, readOnly()
		}
		if n.ID != "" {
			if !validID(n.ID) {
				return 0, invalid("id: expected a UUID")
			}
			// Sending the same id again gives back the todo it made.
			if t := b.find(n.ID); t != nil {
				return http.StatusOK, map[string]any{"todos": []api.Todo{t.Todo}, "idempotent": true}
			}
			// A deletion still to be sent keeps its id here. A second todo
			// under it would be two todos with one id, and the service keeps a
			// deleted todo deleted whatever reaches it later.
			for i := range b.st.Todos {
				if b.st.Todos[i].ID == n.ID {
					return 0, problem{http.StatusConflict, api.CodeConflict, "id: a deleted todo has this id"}
				}
			}
		}
		made := []api.Todo{}
		for _, line := range lines {
			parsed := Parsed{Title: line}
			if n.Parse == nil || *n.Parse {
				parsed = b.reading(line, read)
			}
			t, p := b.newTodo(n, line, listID, parsed)
			if p != nil {
				return 0, *p
			}
			b.st.Todos = append(b.st.Todos, t)
			made = append(made, t.Todo)
		}
		return http.StatusCreated, map[string]any{"todos": made}
	})
}

// newTodo builds a todo from a create request the way the service does:
// dates in the text are read unless the request gives its own, and a
// reminder is set from whichever date it ends up with. parsed is how the
// line reads.
func (b *Backend) newTodo(n api.NewTodo, line, listID string, parsed Parsed) (Todo, *problem) {
	now := b.now()
	raw := n.Title
	t := Todo{New: true, Todo: api.Todo{
		ID: n.ID, ListID: listID, Title: parsed.Title, RawInput: &raw,
		Notes: n.Notes, AssigneeID: n.AssigneeID, CreatedAt: now, UpdatedAt: now,
	}}
	if t.ID == "" {
		t.ID = NewID()
	}
	if n.SortOrder != nil {
		t.SortOrder = *n.SortOrder
	}
	if parsed.Recurrence != "" {
		t.RecurrenceRule = &parsed.Recurrence
	}

	read := func(field string, s *string) (*time.Time, *problem) {
		if s == nil {
			return nil, nil
		}
		at, ok := b.instant(*s)
		if !ok {
			p := invalid(field + ": could not read \"" + *s + "\" as a date or time")
			return nil, &p
		}
		return at, nil
	}
	do, p := read("doAt", n.DoAt)
	if p != nil {
		return t, p
	}
	due, p := read("dueAt", n.DueAt)
	if p != nil {
		return t, p
	}
	remind, p := read("remindAt", n.RemindAt)
	if p != nil {
		return t, p
	}
	completed, p := read("completedAt", n.CompletedAt)
	if p != nil {
		return t, p
	}
	t.DueAt = due
	switch {
	case do != nil:
		b.setDo(&t, do, true)
	case n.ScheduledDay != nil:
		at, ok := b.morning(*n.ScheduledDay)
		if !ok {
			p := invalid("scheduledDay: expected YYYY-MM-DD")
			return t, &p
		}
		b.setDo(&t, at, false)
	case parsed.DoAt != nil && parsed.HasTime:
		b.setDo(&t, parsed.DoAt, true)
	case parsed.DoAt != nil:
		at, _ := b.morning(parsed.DoAt.In(b.loc()).Format("2006-01-02"))
		b.setDo(&t, at, false)
	}
	if do != nil || due != nil {
		// Explicit dates mean the text was not meant to be read for one.
		t.Title = line
	}
	t.RemindAt = remind
	if remind == nil {
		t.RemindAt = b.defaultReminder(&t)
	}
	if n.IsCompleted != nil && *n.IsCompleted {
		t.IsCompleted = true
		at := now
		if completed != nil {
			at = *completed
		}
		t.CompletedAt = &at
	}
	t.Title = StoredTitle(t.Title)
	return t, nil
}

func (b *Backend) todo(w http.ResponseWriter, r *http.Request, seg []string) {
	id, action := seg[0], ""
	if len(seg) > 1 {
		action = seg[1]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		b.locked(w, func() (int, any) {
			t := b.find(id)
			if t == nil {
				return 0, notFound()
			}
			return http.StatusOK, map[string]any{"todo": t.Todo}
		})
	case action == "" && r.Method == http.MethodPatch:
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			fail(w, http.StatusBadRequest, "invalid_request", "expected a JSON object")
			return
		}
		b.change(w, func() (int, any) { return b.patchTodo(id, patch) })
	case action == "" && r.Method == http.MethodDelete:
		b.change(w, func() (int, any) { return b.deleteTodo(id) })
	case action == "complete" && r.Method == http.MethodPost:
		b.change(w, func() (int, any) {
			return b.patchTodo(id, map[string]json.RawMessage{"isCompleted": json.RawMessage("true")})
		})
	case action == "uncomplete" && r.Method == http.MethodPost:
		b.change(w, func() (int, any) {
			return b.patchTodo(id, map[string]json.RawMessage{"isCompleted": json.RawMessage("false")})
		})
	case (action == "claim" || action == "release") && r.Method == http.MethodPost:
		b.assignment(w, r, id)
	case action == "checklist" && r.Method == http.MethodGet:
		b.locked(w, func() (int, any) {
			t := b.find(id)
			if t == nil {
				return 0, notFound()
			}
			items := t.Steps
			if items == nil {
				items = []api.ChecklistItem{}
			}
			return http.StatusOK, map[string]any{"items": items}
		})
	case action == "checklist" && r.Method == http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.change(w, func() (int, any) {
			t := b.find(id)
			title := StoredTitle(body.Title)
			switch {
			case t == nil:
				return 0, notFound()
			case TextUnits(body.Title) > MaxTitle:
				return 0, invalid("a step title is at most 500 characters")
			case title == "":
				return 0, invalid("a step needs a title")
			case len(t.Steps) >= maxSteps:
				return 0, invalid("a checklist holds at most 50 steps")
			}
			if l := b.list(t.ListID); l != nil && l.ReadOnly {
				return 0, readOnly()
			}
			now := b.now()
			item := api.ChecklistItem{ID: NewID(), TodoID: t.ID, Title: title, Position: len(t.Steps), CreatedAt: now}
			t.Base = t.based()
			t.Steps = append(t.Steps, item)
			t.touch(now, fChecklist)
			return http.StatusCreated, map[string]any{"item": item}
		})
	default:
		fail(w, http.StatusNotFound, api.CodeNotFound, "not found")
	}
}

// assignment takes a todo in a shared list or hands it back. Only the
// service can say who won when two people try at once, so this is never
// settled here or kept to send later, as an edit would be: with no account
// or no connection it is refused and nothing changes. Otherwise it goes to
// the service, and the todo it answers with, the winner's included, is kept
// here before the answer is passed on.
func (b *Backend) assignment(w http.ResponseWriter, r *http.Request, id string) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	b.mu.Lock()
	t := b.find(id)
	var l *List
	unsent := false
	if t != nil {
		l = b.list(t.ListID)
		unsent = t.New || len(t.Dirty) > 0 || l != nil && l.Local
	}
	signed := b.signedIn()
	b.mu.Unlock()
	switch {
	case t == nil:
		fail(w, http.StatusNotFound, api.CodeNotFound, "not found")
		return
	case l != nil && l.IsPersonal:
		fail(w, http.StatusBadRequest, api.CodePersonalList, "only todos in shared lists can be claimed or returned to Anyone")
		return
	case !signed:
		fail(w, http.StatusForbidden, "forbidden", "taking a todo needs an Evado account, which decides who has it; sign in from Account, Connection")
		return
	}
	if unsent {
		// The service judges the todo it holds, so a change made here that
		// it has not heard yet, such as an assignment, has to reach it first.
		b.syncFresh(r.Context())
		b.mu.Lock()
		t = b.find(id)
		unsent = t != nil && t.New
		b.mu.Unlock()
		if unsent {
			fail(w, http.StatusServiceUnavailable, api.CodeOffline, "this todo has not reached Evado yet, and only Evado can say who has it; nothing changed, so try again once it has synced")
			return
		}
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	b.proxy(rec, r, path)
	var out struct {
		Code string    `json:"code"`
		Todo *api.Todo `json:"todo"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &out)
	if rec.status == http.StatusServiceUnavailable && out.Code == api.CodeOffline {
		fail(w, rec.status, api.CodeOffline, "could not reach Evado, which decides who has a todo when two people try at once; nothing changed, so try again when you are online")
		return
	}
	if out.Todo != nil && (rec.status < 300 || rec.status == http.StatusConflict) {
		b.mu.Lock()
		if b.keepAnswer(*out.Todo) {
			_ = b.persist()
		}
		b.mu.Unlock()
	}
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes())
}

// keepAnswer takes a todo the service just sent in place of the one kept
// here, keeping its checklist, which such an answer leaves out, and any
// change made here that the service has not heard yet. It reports whether
// there was a todo to replace. The caller holds the lock.
func (b *Backend) keepAnswer(got api.Todo) bool {
	cur := b.find(got.ID)
	if cur == nil {
		return false
	}
	in := Todo{Todo: got, Steps: cur.Steps}
	if cur.New || len(cur.Dirty) > 0 {
		in = keepPending(in, *cur)
	}
	in.Refused, in.Spawned, in.NextOnSync = cur.Refused, cur.Spawned, cur.NextOnSync
	in.ImportedID, in.ImportStarted, in.ImportBase = cur.ImportedID, cur.ImportStarted, cur.ImportBase
	*cur = in
	return true
}

// deleteTodo marks a todo deleted. The caller holds the lock.
func (b *Backend) deleteTodo(id string) (int, any) {
	t := b.find(id)
	if t == nil {
		return 0, notFound()
	}
	if l := b.list(t.ListID); l != nil && l.ReadOnly {
		return 0, readOnly()
	}
	now := b.now()
	t.Base = t.based()
	t.DeletedAt = &now
	t.touch(now, fDeleted)
	gone := t.Todo
	if t.New && !t.ImportStarted {
		// The service never heard of it, so there is nothing to tell.
		b.drop(id)
	}
	return http.StatusOK, map[string]any{"todo": gone}
}

// drop removes a todo outright. The caller holds the lock.
func (b *Backend) drop(id string) { b.dropAll(map[string]bool{id: true}) }

// dropAll removes every todo with one of the ids in a single pass. The
// caller holds the lock.
func (b *Backend) dropAll(ids map[string]bool) {
	kept := b.st.Todos[:0]
	for _, t := range b.st.Todos {
		if !ids[t.ID] {
			kept = append(kept, t)
		} else if t.New {
			if b.st.ImportedIDs == nil {
				b.st.ImportedIDs = map[string]string{}
			}
			source := t.ImportedID
			if source == "" {
				source = t.ID
			}
			b.st.ImportedIDs[source] = t.ID
		}
	}
	b.st.Todos = kept
}

// completionOnly reports whether a patch only marks a todo done or not,
// which is all a read-only list allows.
func completionOnly(patch map[string]json.RawMessage) bool {
	for k := range patch {
		if k != "isCompleted" && k != "completedAt" {
			return false
		}
	}
	return true
}

func isNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

// maxSteps is the most steps a checklist holds.
const maxSteps = 50

// remindAt sets when a todo reminds. A new time re-arms a reminder that
// already went out, as the service does; the same time again leaves it
// delivered.
func remindAt(t *Todo, at *time.Time) {
	if !sameInstant(t.RemindAt, at) {
		t.RemindedAt = nil
	}
	t.RemindAt = at
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// patchTodo applies a PATCH. The caller holds the lock.
func (b *Backend) patchTodo(id string, patch map[string]json.RawMessage) (int, any) {
	t := b.find(id)
	if t == nil {
		return 0, notFound()
	}
	if l := b.list(t.ListID); l != nil && l.ReadOnly && !completionOnly(patch) {
		return 0, readOnly()
	}
	// Work on a copy, so a patch that turns out to be invalid half way
	// through changes nothing.
	next := *t
	now := b.now()
	var touched []string
	str := func(key string) (string, bool) {
		var s string
		return s, json.Unmarshal(patch[key], &s) == nil
	}
	when := func(key string) (*time.Time, *problem) {
		if isNull(patch[key]) {
			return nil, nil
		}
		s, _ := str(key)
		at, ok := b.instant(s)
		if !ok {
			p := invalid(key + ": could not read \"" + s + "\" as a date or time")
			return nil, &p
		}
		return at, nil
	}

	if _, ok := patch["title"]; ok {
		title, _ := str("title")
		if title = StoredTitle(title); title == "" {
			return 0, invalid("a todo needs a title")
		}
		next.Title = title
		touched = append(touched, fTitle)
	}
	if raw, ok := patch["notes"]; ok {
		next.Notes = nil
		if s, ok := str("notes"); ok && !isNull(raw) {
			if TextUnits(s) > 10000 {
				return 0, invalid("notes are at most 10000 characters")
			}
			next.Notes = &s
		}
		touched = append(touched, fNotes)
	}
	if _, ok := patch["dueAt"]; ok {
		at, p := when("dueAt")
		if p != nil {
			return 0, *p
		}
		next.DueAt = at
		touched = append(touched, fDue)
		if _, given := patch["remindAt"]; !given {
			// A deadline picked on purpose reminds at that instant.
			remindAt(&next, at)
			touched = append(touched, fRemind)
		}
	}
	if _, ok := patch["doAt"]; ok {
		at, p := when("doAt")
		if p != nil {
			return 0, *p
		}
		hasTime := true
		// hasTime:false marks a date typed without a time; keep it a do
		// date rather than a midnight.
		if raw, given := patch["hasTime"]; given && at != nil && strings.TrimSpace(string(raw)) == "false" {
			hasTime = false
			at, _ = b.morning(at.Format("2006-01-02"))
		}
		b.setDo(&next, at, hasTime)
		touched = append(touched, fDo)
	}
	if raw, ok := patch["scheduledDay"]; ok {
		if isNull(raw) {
			b.setDo(&next, nil, false)
		} else {
			day, _ := str("scheduledDay")
			at, ok := b.morning(day)
			if !ok {
				return 0, invalid("scheduledDay: expected YYYY-MM-DD")
			}
			b.setDo(&next, at, false)
		}
		touched = append(touched, fDo)
	}
	if _, ok := patch["remindAt"]; ok {
		at, p := when("remindAt")
		if p != nil {
			return 0, *p
		}
		remindAt(&next, at)
		touched = append(touched, fRemind)
	}
	if raw, ok := patch["recurrenceRule"]; ok {
		next.RecurrenceRule = nil
		if !isNull(raw) {
			rule, _ := str("recurrenceRule")
			if !ValidRule(rule) {
				return 0, invalid("recurrenceRule: expected a rule such as 1:week or 1:week:3")
			}
			next.RecurrenceRule = &rule
		}
		touched = append(touched, fRule)
	}
	wasOpen := !t.IsCompleted
	if raw, ok := patch["completedAt"]; ok {
		at, p := when("completedAt")
		if p != nil {
			return 0, *p
		}
		next.CompletedAt, next.IsCompleted = at, !isNull(raw)
		touched = append(touched, fCompleted)
	} else if raw, ok := patch["isCompleted"]; ok {
		done := strings.TrimSpace(string(raw)) == "true"
		switch {
		case done && !next.IsCompleted:
			next.IsCompleted, next.CompletedAt = true, &now
			touched = append(touched, fCompleted)
		case !done && next.IsCompleted:
			next.IsCompleted, next.CompletedAt, next.RemindedAt = false, nil, nil
			touched = append(touched, fCompleted)
		}
	}
	if raw, ok := patch["sortOrder"]; ok {
		if err := json.Unmarshal(raw, &next.SortOrder); err != nil {
			return 0, invalid("sortOrder: expected a whole number")
		}
		touched = append(touched, fSort)
	}
	if raw, ok := patch["assigneeId"]; ok {
		next.AssigneeID = nil
		if s, ok := str("assigneeId"); ok && !isNull(raw) && s != "" {
			next.AssigneeID = &s
		}
		touched = append(touched, fAssignee)
	}
	if _, ok := patch["listId"]; ok {
		to, _ := str("listId")
		l := b.list(to)
		switch {
		case l == nil:
			return 0, problem{http.StatusNotFound, api.CodeNotFound, "unknown list"}
		case l.ReadOnly:
			return 0, readOnly()
		}
		if to != next.ListID {
			next.ListID = to
			touched = append(touched, fList)
		}
	}

	if len(touched) > 0 {
		// t is still as it was before the patch.
		next.Base = t.based()
		next.touch(now, touched...)
	}
	*t = next
	out := map[string]any{"todo": t.Todo, "next": nil}
	// With an account the service makes the next occurrence of a repeating
	// todo when it hears of the completion. With none, it is made here.
	switch {
	case !wasOpen || !t.IsCompleted:
	case b.signedIn():
		t.NextOnSync = t.New && t.RecurrenceRule != nil
	default:
		// spawnNext appends to the todos, which may move t.
		done := t.ID
		if nx := b.spawnNext(*t); nx != nil {
			out["next"] = nx.Todo
			b.find(done).Spawned = true
		}
	}
	return http.StatusOK, out
}

// spawnNext makes the next occurrence of a repeating todo that was just
// completed, at the same time of day, with its checklist copied and every
// step open. The caller holds the lock.
func (b *Backend) spawnNext(done Todo) *Todo {
	if done.RecurrenceRule == nil || !ValidRule(*done.RecurrenceRule) || done.Spawned {
		return nil
	}
	if l := b.list(done.ListID); l != nil && l.ReadOnly {
		return nil
	}
	now := b.now()
	hour, minute := 9, 0
	if base := effective(&done); base != nil && done.HasTime {
		w := base.In(b.loc())
		hour, minute = w.Hour(), w.Minute()
	}
	at := NextOccurrence(*done.RecurrenceRule, b.loc(), now, hour, minute)
	nx := Todo{New: true, Todo: api.Todo{
		ID: NewID(), ListID: done.ListID, AssigneeID: done.AssigneeID, Title: done.Title,
		Notes: done.Notes, RecurrenceRule: done.RecurrenceRule, CreatedAt: now, UpdatedAt: now,
	}}
	b.setDo(&nx, &at, done.HasTime)
	nx.RemindAt = b.defaultReminder(&nx)
	for i, s := range done.Steps {
		if i == maxSteps {
			break
		}
		nx.Steps = append(nx.Steps, api.ChecklistItem{ID: NewID(), TodoID: nx.ID, Title: s.Title, Position: i, CreatedAt: now})
	}
	b.st.Todos = append(b.st.Todos, nx)
	return &b.st.Todos[len(b.st.Todos)-1]
}

// step renames, ticks or removes one checklist step.
func (b *Backend) step(w http.ResponseWriter, r *http.Request, id string) {
	var patch map[string]json.RawMessage
	if r.Method == http.MethodPatch {
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			fail(w, http.StatusBadRequest, "invalid_request", "the body is not valid JSON")
			return
		}
	}
	b.change(w, func() (int, any) {
		for i := range b.st.Todos {
			t := &b.st.Todos[i]
			if t.DeletedAt != nil {
				continue
			}
			for j := range t.Steps {
				if t.Steps[j].ID != id {
					continue
				}
				frozen := false
				if l := b.list(t.ListID); l != nil {
					frozen = l.ReadOnly
				}
				now := b.now()
				switch r.Method {
				case http.MethodDelete:
					if frozen {
						return 0, readOnly()
					}
					t.Base = t.based()
					t.Steps = append(t.Steps[:j:j], t.Steps[j+1:]...)
					for k := range t.Steps {
						t.Steps[k].Position = k
					}
					t.touch(now, fChecklist)
					return http.StatusOK, map[string]any{"ok": true}
				case http.MethodPatch:
					// The whole patch is checked before any of it is applied,
					// so a rejected one leaves the step as it was.
					var title string
					titled, ticked := false, false
					var doneAt *time.Time
					if raw, ok := patch["title"]; ok {
						// In a read-only list steps can be ticked, not renamed.
						if frozen {
							return 0, readOnly()
						}
						if json.Unmarshal(raw, &title) != nil || StoredTitle(title) == "" || TextUnits(title) > MaxTitle {
							return 0, invalid("a step needs a title")
						}
						titled = true
					}
					if raw, ok := patch["completedAt"]; ok {
						ticked = true
						if !isNull(raw) {
							var at time.Time
							if json.Unmarshal(raw, &at) != nil {
								return 0, invalid("completedAt: expected a timestamp")
							}
							doneAt = &at
						}
					}
					t.Base = t.based()
					if titled {
						t.Steps[j].Title = StoredTitle(title)
					}
					if ticked {
						t.Steps[j].CompletedAt = doneAt
					}
					t.touch(now, fChecklist)
					return http.StatusOK, map[string]any{"item": t.Steps[j]}
				}
			}
		}
		return 0, notFound()
	})
}

// -- lists ------------------------------------------------------------------

// maxListName is counted in UTF-16 units, as the service counts it.
const maxListName = 120

// listName is a list name as the service would store it, or a problem.
func listName(raw string) (string, *problem) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", &problem{http.StatusBadRequest, "invalid_request", "a list needs a name"}
	case TextUnits(name) > maxListName:
		return "", &problem{http.StatusBadRequest, "invalid_request", "a list name is at most 120 characters"}
	}
	return name, nil
}

func (b *Backend) createList(w http.ResponseWriter, r *http.Request, path string) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &body)
	b.mu.Lock()
	signed := b.signedIn()
	b.mu.Unlock()
	if signed {
		// With an account the list is made there when it can be, so it has
		// its real id from the start; the sync that follows brings it here.
		// With no connection it is made here and created there on the next
		// sync, like a list made before signing in.
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if b.forward(w, r, path) {
			return
		}
	}
	b.change(w, func() (int, any) {
		name, p := listName(body.Name)
		switch {
		case p != nil:
			return 0, *p
		case len(b.st.Lists) >= 25:
			return 0, invalid("too many lists")
		}
		l := List{Local: true, List: api.List{
			ID: NewID(), Name: name, OwnerID: b.st.User.ID, Role: "owner", CreatedAt: b.now(),
		}}
		b.st.Lists = append(b.st.Lists, l)
		return http.StatusCreated, map[string]any{"list": l.List}
	})
}

// changeList renames or deletes a list. Either is done here at once and,
// for a list the service holds, sent on the next sync, so it works with no
// connection. The service's rules are checked first, so what it would
// refuse is refused now rather than undone later.
func (b *Backend) changeList(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Name string `json:"name"`
	}
	if r.Method == http.MethodPatch {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	b.change(w, func() (int, any) {
		l := b.list(id)
		if l == nil {
			return 0, notFound()
		}
		held := b.st.UserID != "" && !l.Local
		if r.Method == http.MethodPatch {
			name, p := listName(body.Name)
			switch {
			case p != nil:
				return 0, *p
			case held && l.Role != "owner":
				return 0, problem{http.StatusForbidden, "forbidden", "only the owner can rename the list"}
			}
			if held && !l.Renamed {
				l.Renamed, l.Was = true, l.Name
			}
			l.Name = name
			if l.Renamed && l.Was == name {
				l.Renamed, l.Was = false, ""
			}
			return http.StatusOK, map[string]any{"list": l.List}
		}
		switch {
		case l.IsPersonal:
			return 0, problem{http.StatusBadRequest, "personal_list", "the personal list cannot be deleted"}
		case held && l.Role != "owner":
			return 0, problem{http.StatusForbidden, "forbidden", "only the owner can delete the list"}
		}
		b.removeList(id)
		return http.StatusOK, map[string]any{"ok": true}
	})
}

// removeList takes a list and its todos away here. A list the service
// holds is queued to be deleted there.
//
// The service deletes the todos that are in the list when the deletion
// reaches it, so this only drops them here. Three kinds are kept back: a
// todo moved out of the list before it was deleted is not in it any more,
// and its move goes up before the deletion does; a todo moved into it
// and not yet sent, which the service still has in another list, goes up
// as a deletion of its own, so it is not left alive there and gone here;
// and a todo deleted on its own and not yet sent still goes up as that,
// so it stays deleted if the list's deletion is undone.
// The caller holds the lock.
func (b *Backend) removeList(id string) {
	var gone *List
	kept := make([]List, 0, len(b.st.Lists))
	for i := range b.st.Lists {
		if b.st.Lists[i].ID == id {
			gone = &b.st.Lists[i]
			continue
		}
		kept = append(kept, b.st.Lists[i])
	}
	if gone == nil {
		return
	}
	held := b.st.UserID != "" && !gone.Local
	if held {
		l := *gone
		if l.Renamed {
			l.Name, l.Renamed, l.Was = l.Was, false, ""
		}
		b.st.Deleted = append(b.st.Deleted, l)
	}
	b.st.Lists = kept
	now := b.now()
	todos := b.st.Todos[:0]
	for _, t := range b.st.Todos {
		if t.ListID != id {
			todos = append(todos, t)
			continue
		}
		switch {
		case !held || t.New:
		case t.DeletedAt != nil && t.pending():
			todos = append(todos, t)
		case contains(t.Dirty, fList) && t.DeletedAt == nil:
			t.DeletedAt = &now
			t.Dirty = nil
			t.touch(now, fDeleted)
			todos = append(todos, t)
		}
	}
	b.st.Todos = todos
}

// NotSyncedMessage is what is said of a list that the account does not have
// yet when someone asks to share it.
const NotSyncedMessage = "this list has not been synced yet; try again after a sync"

// Unsynced is the ids of the lists the account does not have yet: made
// offline, or before signing in, and created there by the next sync. With
// no account there are none, as every list is only here.
func (b *Backend) Unsynced() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.signedIn() {
		return nil
	}
	out := map[string]bool{}
	for _, l := range b.st.Lists {
		if l.Local {
			out[l.ID] = true
		}
	}
	return out
}

// share passes an invitation, or a request for an invite link, on to the
// service. A list the service has not heard of cannot be shared yet, and
// asking it would only be answered as not found.
func (b *Backend) share(w http.ResponseWriter, r *http.Request, path, id string) {
	if b.Unsynced()[id] {
		fail(w, http.StatusConflict, api.CodeListNotSynced, NotSyncedMessage)
		return
	}
	b.proxy(w, r, path)
}

// members answers from what the last sync brought, which is what lets a
// todo be assigned with no connection.
func (b *Backend) members(w http.ResponseWriter, r *http.Request, path, id string) {
	b.mu.Lock()
	l := b.list(id)
	var cached []api.Member
	if l != nil {
		cached = l.Members
	}
	signed := b.signedIn()
	user := b.st.User
	b.mu.Unlock()
	switch {
	case l == nil:
		fail(w, http.StatusNotFound, api.CodeNotFound, "not found")
	case len(cached) > 0:
		reply(w, http.StatusOK, map[string]any{"members": cached})
	case signed && !l.Local:
		b.proxy(w, r, path)
	default:
		// A list that only lives here has one member: whoever is using it.
		reply(w, http.StatusOK, map[string]any{"members": []api.Member{{UserID: user.ID, Email: user.Email, Role: "owner"}}})
	}
}

// fullRead answers a /sync that sends nothing and asks from the start,
// which is how a command lists every todo to match a shortened id. With an
// account it is the service's to answer. Without one there is no change
// log to page through or push to, so only that full read is answered, in
// one page, with no cursor to carry on from.
func (b *Backend) fullRead(w http.ResponseWriter, r *http.Request, path string) {
	b.mu.Lock()
	signed := b.signedIn()
	b.mu.Unlock()
	if signed {
		b.proxy(w, r, path)
		return
	}
	var body struct {
		Since   string            `json:"since"`
		Changes []json.RawMessage `json:"changes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Since != "" || len(body.Changes) > 0 {
		fail(w, http.StatusForbidden, "forbidden", "the todos on this computer have no change log to sync with; that needs an Evado account")
		return
	}
	b.locked(w, func() (int, any) {
		out := []api.SyncTodo{}
		for _, t := range b.st.Todos {
			if t.DeletedAt == nil {
				steps := t.Steps
				if steps == nil {
					steps = []api.ChecklistItem{}
				}
				out = append(out, api.SyncTodo{Todo: t.Todo, Checklist: steps})
			}
		}
		return http.StatusOK, api.SyncResponse{Todos: out, Lists: b.lists(), HasMore: false}
	})
}
