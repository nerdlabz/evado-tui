// Package ops is what you can do to an Evado account, as plain functions
// that take and return data. The command line is a thin layer
// over it, so a todo means the same thing however it is reached.
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/dates"
)

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// Ops runs operations against one account.
type Ops struct {
	C *api.Client

	loc   *time.Location
	lists []api.List
	me    *api.User
}

// New returns an Ops for a client.
func New(c *api.Client) *Ops { return &Ops{C: c} }

// defaultLimit is how many todos a listing returns unless told otherwise.
const defaultLimit = 200

// -- account ----------------------------------------------------------------

// loadMe fetches the account and its lists once.
func (o *Ops) loadMe(ctx context.Context) error {
	if o.me != nil {
		return nil
	}
	u, lists, err := o.C.Me(ctx)
	if err != nil {
		return err
	}
	o.me, o.lists = &u, lists
	// LoadLocation answers UTC for "" and the machine's zone for "Local",
	// neither of which is the account's clock.
	if u.TZ != "" && u.TZ != "Local" {
		if loc, lerr := time.LoadLocation(u.TZ); lerr == nil {
			o.loc = loc
		}
	}
	return nil
}

// Location is the account's timezone, which is what dates are read in. It
// fails rather than guess: a time read in the wrong zone is saved wrong.
func (o *Ops) Location(ctx context.Context) (*time.Location, error) {
	if err := o.loadMe(ctx); err != nil {
		return nil, &Error{Code: codeUsage, Message: "could not load the account timezone, so the time cannot be read", Cause: err}
	}
	if o.loc == nil {
		return nil, usagef("the account timezone %q is not one this computer knows, so the time cannot be read", o.me.TZ)
	}
	return o.loc, nil
}

// Me returns the account and its lists.
func (o *Ops) Me(ctx context.Context) (api.User, []api.List, error) {
	if err := o.loadMe(ctx); err != nil {
		return api.User{}, nil, err
	}
	return *o.me, o.lists, nil
}

// Lists returns the account's lists.
func (o *Ops) Lists(ctx context.Context) ([]api.List, error) {
	lists, err := o.C.ListLists(ctx)
	if err != nil {
		return nil, err
	}
	o.lists = lists
	return lists, nil
}

// ListID resolves a list given by name or id. Names match without regard to
// case. An empty reference means no list, which callers read as the default.
func (o *Ops) ListID(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if o.lists == nil {
		if _, err := o.Lists(ctx); err != nil {
			return "", err
		}
	}
	for _, l := range o.lists {
		if l.ID == ref {
			return l.ID, nil
		}
	}
	for _, l := range o.lists {
		if strings.EqualFold(l.Name, ref) {
			return l.ID, nil
		}
	}
	names := make([]string, 0, len(o.lists))
	for _, l := range o.lists {
		names = append(names, l.Name)
	}
	return "", notFoundf("no list called %q (lists: %s)", ref, strings.Join(names, ", "))
}

// ListName is the display name for a list id.
func (o *Ops) ListName(id string) string {
	for _, l := range o.lists {
		if l.ID == id {
			return l.Name
		}
	}
	return ""
}

// -- todos ------------------------------------------------------------------

// AddInput creates todos. Text may hold several lines, one todo each, and
// dates written in it ("tomorrow 3pm") are read by the server in the
// account timezone.
type AddInput struct {
	Text  string
	List  string
	Notes string
	// ID is an id chosen by the caller, for a create that can be retried.
	ID string
}

// Add creates one todo, or several when Text has several lines.
func (o *Ops) Add(ctx context.Context, in AddInput) ([]api.Todo, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, usagef("nothing to add: give the text of the todo")
	}
	if in.ID != "" && strings.Contains(text, "\n") {
		return nil, usagef("an id can only be set when adding a single todo")
	}
	listID, err := o.ListID(ctx, in.List)
	if err != nil {
		return nil, err
	}
	batches, err := Captures(text)
	if err != nil {
		return nil, err
	}
	n := api.NewTodo{ID: in.ID, ListID: listID}
	if notes := strings.TrimSpace(in.Notes); notes != "" {
		n.Notes = &in.Notes
	}
	// Batches go one at a time so the todos keep the order of the lines.
	// When one fails, the todos already made are returned with the error:
	// they exist, and a script re-running the whole input would add them
	// twice without knowing which went in.
	var added []api.Todo
	for _, b := range batches {
		n.Title = b
		made, err := o.C.CreateTodos(ctx, n)
		added = append(added, made...)
		if err != nil {
			return added, err
		}
	}
	return added, nil
}

// maxCapture is the longest title the service reads. It refuses a longer
// one outright, even when each of its lines would be a todo of its own, and
// it counts UTF-16 units. maxCaptureLines is how many lines of one title it
// turns into todos: it refuses more with too_many_lines and creates nothing.
const (
	maxCapture      = 2000
	maxCaptureLines = 20
)

// Captures cuts text into titles the service will take whole, splitting
// only between lines, which the service would have split into todos anyway.
// It is how any caller turns a long paste into the requests that carry it.
func Captures(text string) ([]string, error) {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if units(text) <= maxCapture && len(lines) <= maxCaptureLines {
		return []string{text}, nil
	}
	var out []string
	var cur []string
	size := 0
	for i, line := range lines {
		n := units(line)
		if n > maxCapture {
			return nil, usagef("line %d is longer than the %d characters a todo can have", lineNumber(text, i), maxCapture)
		}
		if len(cur) > 0 && (len(cur) == maxCaptureLines || size+1+n > maxCapture) {
			out = append(out, strings.Join(cur, "\n"))
			cur, size = nil, 0
		}
		if len(cur) > 0 {
			size++
		}
		cur = append(cur, line)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out, nil
}

// lineNumber is the 1-based line of text holding its nth non-blank line,
// so an error points at the line as the person typed it.
func lineNumber(text string, nth int) int {
	seen := -1
	for i, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			if seen++; seen == nth {
				return i + 1
			}
		}
	}
	return nth + 1
}

func units(s string) int { return len(utf16.Encode([]rune(s))) }

// ListInput filters a listing.
type ListInput struct {
	Status   string
	List     string
	Query    string
	Assigned string
	// Limit is the most todos to return, across as many pages as that takes.
	// Zero means defaultLimit.
	Limit int
	// Cursor resumes a listing where an earlier one stopped: it is that
	// listing's NextCursor, passed back with the same filters.
	Cursor string
}

// Listing is the todos a listing found.
type Listing struct {
	Todos []api.Todo
	// NextCursor is empty when every match was returned. Otherwise more
	// match, and passing it back as ListInput.Cursor, with the same filters,
	// carries on after the last todo returned.
	NextCursor string
}

// ListTodos returns todos matching a filter, up to in.Limit of them. With no
// status it lists the open ones. The service answers in pages of at most
// api.MaxPageSize, so a larger limit is read page by page. If any page fails
// the call fails with nothing returned, so a short list is never mistaken
// for a whole one.
func (o *Ops) ListTodos(ctx context.Context, in ListInput) (Listing, error) {
	status := in.Status
	if status == "" {
		status = api.StatusOpen
	}
	if !slices.Contains(api.Statuses(), status) {
		return Listing{}, usagef("unknown status %q (use one of: %s)", status, strings.Join(api.Statuses(), ", "))
	}
	switch in.Assigned {
	case "", "me", "anyone":
	default:
		return Listing{}, usagef("assigned must be me or anyone, not %q", in.Assigned)
	}
	listID, err := o.ListID(ctx, in.List)
	if err != nil {
		return Listing{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	f := api.TodoFilter{Status: status, ListID: listID, Query: in.Query, Assigned: in.Assigned, Cursor: in.Cursor}
	out := Listing{Todos: []api.Todo{}}
	for len(out.Todos) < limit {
		// The last page asks for just what is still wanted, so the cursor it
		// ends with comes right after the last todo returned.
		f.Limit = min(limit-len(out.Todos), api.MaxPageSize)
		page, err := o.C.ListTodosPage(ctx, f)
		if err != nil {
			return Listing{}, err
		}
		out.Todos = append(out.Todos, page.Todos...)
		out.NextCursor = page.NextCursor
		if page.NextCursor == "" {
			break
		}
		// A cursor that does not move would be asked for again forever.
		if page.NextCursor == f.Cursor || len(page.Todos) == 0 {
			return Listing{}, &Error{
				Code:    codeListStalled,
				Message: "the list cannot be followed: the server says more match but its cursor did not move",
				Exit:    ExitServer,
			}
		}
		f.Cursor = page.NextCursor
	}
	return out, nil
}

// Resolve finds the todo a reference points at. A reference is a full id or
// the start of one, so the short ids a listing prints work. An exact id wins;
// otherwise the start must match exactly one todo in a complete listing of
// the account. To resolve several references, use a Resolver so they share
// one listing.
func (o *Ops) Resolve(ctx context.Context, ref string) (api.Todo, error) {
	return o.Resolver().Resolve(ctx, ref)
}

// ResolveAll resolves every reference before returning any of them, against
// one listing of the account. It stops at the first reference that fails.
func (o *Ops) ResolveAll(ctx context.Context, refs []string) ([]api.Todo, error) {
	r := o.Resolver()
	out := make([]api.Todo, 0, len(refs))
	for _, ref := range refs {
		t, err := r.Resolve(ctx, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Resolver resolves references against a listing of every todo, fetched the
// first time a shortened id needs it and reused after that.
type Resolver struct {
	o      *Ops
	loaded bool
	all    []api.Todo
	err    error
}

// Resolver returns a Resolver that shares one listing across its calls.
func (o *Ops) Resolver() *Resolver { return &Resolver{o: o} }

// applied brings the listing in line with a change made through this
// resolver, so a later reference in the same batch sees the todo as it is
// now rather than as it was listed: a repeated id is already done, a
// completed repeating todo has a next occurrence to be found, and a deleted
// todo is gone.
func (r *Resolver) applied(t api.Todo, next *api.Todo) {
	if !r.loaded || r.err != nil {
		return
	}
	for i := range r.all {
		if r.all[i].ID == t.ID {
			r.all[i] = t
		}
	}
	if next != nil {
		r.all = append(r.all, *next)
	}
}

// removed drops a deleted todo from the listing.
func (r *Resolver) removed(id string) {
	r.all = slices.DeleteFunc(r.all, func(t api.Todo) bool { return t.ID == id })
}

// Resolve is Ops.Resolve for this resolver's listing.
func (r *Resolver) Resolve(ctx context.Context, ref string) (api.Todo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return api.Todo{}, usagef("give a todo id")
	}
	if t, err := r.o.C.GetTodo(ctx, ref); err == nil {
		return t, nil
	} else if !isLookupMiss(err) {
		return api.Todo{}, err
	}
	if !r.loaded {
		r.all, r.err = r.o.allTodos(ctx)
		r.loaded = true
	}
	if r.err != nil {
		return api.Todo{}, r.err
	}
	var hits []api.Todo
	for _, t := range r.all {
		if strings.HasPrefix(t.ID, ref) {
			hits = append(hits, t)
		}
	}
	switch len(hits) {
	case 0:
		return api.Todo{}, notFoundf("no todo has an id starting with %q", ref)
	case 1:
		return hits[0], nil
	}
	cands := make([]string, 0, len(hits))
	for _, t := range hits {
		cands = append(cands, t.ID+"  "+t.Title)
	}
	return api.Todo{}, &Error{
		Code:       codeAmbiguous,
		Message:    sprintf("%q starts %d todos; give more of the id", ref, len(hits)),
		Candidates: cands,
	}
}

// maxFeedPages bounds a walk of the change feed, so a server that never
// says it is done cannot hold a command forever. At 200 a page it covers
// 100,000 todos.
const maxFeedPages = 500

// allTodos lists every live todo in the account. It reads the change feed
// rather than a filtered listing, because the feed also reports a todo that
// is deleted while it is being read. If the feed cannot be followed to the
// end, it fails rather than hand back a partial list that would make a
// shortened id look unique or missing.
func (o *Ops) allTodos(ctx context.Context) ([]api.Todo, error) {
	var all []api.Todo
	at := map[string]int{}
	cursor := ""
	for range maxFeedPages {
		page, err := o.pull(ctx, cursor)
		if err != nil {
			if isLookupMiss(err) {
				return nil, incompleteLookup()
			}
			return nil, err
		}
		for _, st := range page.Todos {
			// The feed repeats a todo that changed while it was being read, and
			// reports a deleted one as a tombstone; the last word wins.
			i, seen := at[st.ID]
			switch {
			case st.Tombstoned() && seen:
				all[i] = api.Todo{}
			case st.Tombstoned():
			case seen:
				all[i] = st.Todo
			default:
				at[st.ID] = len(all)
				all = append(all, st.Todo)
			}
		}
		if !page.HasMore {
			return slices.DeleteFunc(all, func(t api.Todo) bool { return t.ID == "" }), nil
		}
		if page.Cursor == "" || page.Cursor == cursor {
			return nil, incompleteLookup()
		}
		cursor = page.Cursor
	}
	return nil, incompleteLookup()
}

func incompleteLookup() error {
	return &Error{
		Code:    codeIncomplete,
		Message: "could not list every todo, so a shortened id cannot be checked against the whole account; give the full id (evado ls -status all -full-ids)",
	}
}

// isLookupMiss reports whether a direct fetch failing means "try it as a
// prefix": the id was not found, or was not shaped like an id at all.
func isLookupMiss(err error) bool {
	if api.IsNotFound(err) {
		return true
	}
	switch api.CodeOf(err) {
	case api.CodeBadRequest, api.CodeInvalidRequest:
		return true
	}
	return false
}

// Detail is a todo with its checklist and list name.
type Detail struct {
	Todo     api.Todo            `json:"todo"`
	Steps    []api.ChecklistItem `json:"steps"`
	ListName string              `json:"list"`
}

// Show returns a todo with its steps.
func (o *Ops) Show(ctx context.Context, ref string) (Detail, error) {
	t, err := o.Resolve(ctx, ref)
	if err != nil {
		return Detail{}, err
	}
	steps, err := o.C.ListChecklist(ctx, t.ID)
	if err != nil {
		return Detail{}, err
	}
	if o.lists == nil {
		_, _ = o.Lists(ctx)
	}
	return Detail{Todo: t, Steps: steps, ListName: o.ListName(t.ListID)}, nil
}

// Change is the outcome of an operation on one todo. Changed is false when
// the todo was already in the state asked for, so repeating a call is safe.
type Change struct {
	Todo    api.Todo  `json:"todo"`
	Next    *api.Todo `json:"next,omitempty"`
	Changed bool      `json:"changed"`
}

// SetDone completes or reopens a todo.
func (o *Ops) SetDone(ctx context.Context, ref string, done bool) (Change, error) {
	return o.SetDoneWith(ctx, o.Resolver(), ref, done)
}

// SetDoneWith is SetDone resolving the reference through r, so a batch of
// references shares one listing of the account. Each reference is still
// resolved and acted on in turn, so the changes happen in the order given
// and a failure belongs to its own reference.
func (o *Ops) SetDoneWith(ctx context.Context, r *Resolver, ref string, done bool) (Change, error) {
	t, err := r.Resolve(ctx, ref)
	if err != nil {
		return Change{}, err
	}
	if t.IsCompleted == done {
		return Change{Todo: t}, nil
	}
	if done {
		updated, next, err := o.C.CompleteTodo(ctx, t.ID)
		if err == nil {
			r.applied(fill(updated, t), next)
		}
		return Change{Todo: fill(updated, t), Next: next, Changed: err == nil}, err
	}
	updated, err := o.C.UncompleteTodo(ctx, t.ID)
	if err == nil {
		r.applied(fill(updated, t), nil)
	}
	return Change{Todo: fill(updated, t), Changed: err == nil}, err
}

// fill keeps the todo we already had when the API's answer is empty.
func fill(got, had api.Todo) api.Todo {
	if got.ID == "" {
		return had
	}
	return got
}

// EditInput changes fields of a todo. A nil field is left alone. For the
// fields that can be empty, the value "none" (or an empty string) clears
// them. The date fields take an ISO date or time, or words the server can
// read, such as "friday" or "5pm tomorrow". A day alone, in digits or in
// words, means the same for every field: Day takes it, At keeps a do date
// with no time of day, and Due and Remind take midnight that day on the
// account's clock. Words that hold no date are refused.
type EditInput struct {
	Title  *string
	Notes  *string
	List   *string
	Day    *string
	At     *string
	Due    *string
	Remind *string
	Repeat *string
	Assign *string
	Done   *bool
}

func clears(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "" || v == "none"
}

// Edit changes a todo and returns it as saved.
func (o *Ops) Edit(ctx context.Context, ref string, in EditInput) (Change, error) {
	t, err := o.Resolve(ctx, ref)
	if err != nil {
		return Change{}, err
	}
	var p api.TodoPatch
	listID := t.ListID

	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return Change{}, usagef("a todo cannot have an empty title")
		}
		p.Title = api.Val(title)
	}
	if in.Notes != nil {
		if clears(*in.Notes) {
			p.Notes = api.Null
		} else {
			p.Notes = api.Val(*in.Notes)
		}
	}
	if in.List != nil {
		id, err := o.ListID(ctx, *in.List)
		if err != nil {
			return Change{}, err
		}
		if id == "" {
			return Change{}, usagef("give the name of the list to move it to")
		}
		p.ListID, listID = api.Val(id), id
	}
	for _, f := range []struct {
		field string
		val   *string
		dst   *json.RawMessage
	}{
		{dates.Day, in.Day, &p.ScheduledDay},
		{dates.DoAt, in.At, &p.DoAt},
		{dates.DueAt, in.Due, &p.DueAt},
		{dates.Remind, in.Remind, &p.RemindAt},
	} {
		if f.val == nil {
			continue
		}
		if clears(*f.val) {
			*f.dst = api.Null
			continue
		}
		v, dateOnly, err := o.readDate(ctx, f.field, strings.TrimSpace(*f.val))
		if err != nil {
			return Change{}, err
		}
		dates.Assign(&p, f.field, v, dateOnly)
	}
	if in.Repeat != nil {
		if clears(*in.Repeat) {
			p.RecurrenceRule = api.Null
		} else {
			p.RecurrenceRule = api.Val(strings.TrimSpace(*in.Repeat))
		}
	}
	if in.Assign != nil {
		who := strings.TrimSpace(*in.Assign)
		if clears(who) || strings.EqualFold(who, "anyone") {
			p.AssigneeID = api.Null
		} else {
			id, err := o.memberID(ctx, listID, who)
			if err != nil {
				return Change{}, err
			}
			p.AssigneeID = api.Val(id)
		}
	}
	if in.Done != nil {
		p.IsCompleted = api.BoolVal(*in.Done)
	}
	if p.Empty() {
		return Change{}, usagef("nothing to change: give at least one field to set")
	}
	updated, next, err := o.C.UpdateTodo(ctx, t.ID, p)
	if err != nil {
		return Change{}, err
	}
	return Change{Todo: fill(updated, t), Next: next, Changed: true}, nil
}

// readDate turns what was typed into a value for a date field, written the
// way the service takes it: an ISO date or time is read on the account's
// clock unless it names its own offset, and anything else is read by the
// server. dateOnly is true when a do time was given as a bare day, in digits
// or in words, so it has no time of day.
func (o *Ops) readDate(ctx context.Context, field, text string) (val string, dateOnly bool, err error) {
	if norm, ok := dates.Normalise(text); ok {
		// The zone is fetched only when it matters, so a time with its own
		// offset, or a date for the do day, costs no request.
		loc := time.UTC
		if dates.NeedsZone(field, norm) {
			if loc, err = o.Location(ctx); err != nil {
				return "", false, err
			}
		}
		val, dateOnly = dates.Wire(field, norm, loc, time.Now())
		return val, dateOnly, nil
	}
	pv, err := o.C.Parse(ctx, text)
	if err != nil {
		return "", false, err
	}
	if problem := dates.ParseProblem(field, text, pv); problem != nil {
		return "", false, usagef("%s", problem)
	}
	// A calendar day from the parser is already the account's day; every
	// other result is an instant that has to be written on the account's
	// clock.
	loc := time.UTC
	if field != dates.Day || pv.ScheduledDay == nil {
		if loc, err = o.Location(ctx); err != nil {
			return "", false, err
		}
	}
	v, dateOnly, ok := dates.FromParse(field, pv, loc)
	if !ok {
		return "", false, usagef("%s", dates.Unreadable(field, text))
	}
	return v, dateOnly, nil
}

// memberID finds a list member by email.
func (o *Ops) memberID(ctx context.Context, listID, email string) (string, error) {
	members, err := o.C.ListMembers(ctx, listID)
	if err != nil {
		return "", err
	}
	for _, m := range members {
		if strings.EqualFold(m.Email, email) {
			return m.UserID, nil
		}
	}
	return "", notFoundf("%q is not a member of that list; add them in the Lists tab first", email)
}

// Delete removes a todo. Without confirm it changes nothing and reports what
// would have gone, so a caller has to say twice that it means it.
func (o *Ops) Delete(ctx context.Context, ref string, confirm bool) (api.Todo, error) {
	return o.DeleteWith(ctx, o.Resolver(), ref, confirm)
}

// DeleteWith is Delete resolving the reference through r, for a batch that
// shares one listing.
func (o *Ops) DeleteWith(ctx context.Context, r *Resolver, ref string, confirm bool) (api.Todo, error) {
	t, err := r.Resolve(ctx, ref)
	if err != nil {
		return api.Todo{}, err
	}
	if !confirm {
		return t, &Error{
			Code:    codeConfirmation,
			Message: sprintf("this would delete %q (%s) for every device; repeat with confirmation to do it", t.Title, t.ID),
		}
	}
	err = o.C.DeleteTodo(ctx, t.ID)
	if err == nil {
		r.removed(t.ID)
	}
	return t, err
}

// Claim takes a todo in a shared list for the caller. Unlike assigning it
// with Edit, it never replaces someone who took it first: the service picks
// one winner, and a loser gets an error that names who has it, with exit
// code 6. Claiming a todo that is already yours changes nothing.
func (o *Ops) Claim(ctx context.Context, ref string) (Change, error) {
	return o.assignment(ctx, ref, o.C.ClaimTodo)
}

// Release hands the caller's todo back to Anyone. If someone else has it by
// now nothing changes, and the error says who. Releasing a todo that is
// already Anyone's changes nothing.
func (o *Ops) Release(ctx context.Context, ref string) (Change, error) {
	return o.assignment(ctx, ref, o.C.ReleaseTodo)
}

func (o *Ops) assignment(ctx context.Context, ref string, call func(context.Context, string) (api.Todo, error)) (Change, error) {
	t, err := o.Resolve(ctx, ref)
	if err != nil {
		return Change{}, err
	}
	got, err := call(ctx, t.ID)
	if err != nil {
		return Change{}, err
	}
	got = fill(got, t)
	// A retry the service already applied answers with the todo unchanged,
	// which is how a repeated call is told apart from one that did something.
	return Change{Todo: got, Changed: !sameAssignee(t.AssigneeID, got.AssigneeID)}, nil
}

func sameAssignee(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Parse shows how the server reads a piece of text, creating nothing.
func (o *Ops) Parse(ctx context.Context, text string) (api.ParsePreview, error) {
	if strings.TrimSpace(text) == "" {
		return api.ParsePreview{}, usagef("give some text to parse")
	}
	return o.C.Parse(ctx, text)
}
