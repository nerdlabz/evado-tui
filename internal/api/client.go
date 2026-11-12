package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the production Evado API root.
const DefaultBaseURL = "https://evado.app/api/v1"

// legacyHost is where production used to live. It now only redirects, and
// Go does not resend the Authorization header across hosts, so a client
// still pointed here would see its valid token rejected.
const legacyHost = "evado.nerdlabz.com"

// Error codes the API can return.
const (
	CodeBadRequest           = "bad_request"
	CodeInvalidRequest       = "invalid_request"
	CodeUnauthorized         = "unauthorized"
	CodeForbidden            = "forbidden"
	CodeInsufficientScope    = "insufficient_scope"
	CodeSessionRequired      = "session_required"
	CodeNotFound             = "not_found"
	CodeListReadOnly         = "list_read_only"
	CodeConfirmationMismatch = "confirmation_mismatch"
	CodeConflict             = "conflict"
	CodeRateLimited          = "rate_limited"
	CodeUpstreamFailed       = "upstream_failed"
	CodeInternal             = "internal"
	CodeRedirected           = "redirected"
	// CodeOffline is what the copy of the service kept on this computer
	// answers when the real one cannot be reached.
	CodeOffline = "offline"
	// CodeListNotSynced is what that copy answers to sharing a list the
	// account does not have yet.
	CodeListNotSynced = "list_not_synced"

	// What inviting someone can be refused for.
	CodeUserNotFound     = "user_not_found"
	CodeAlreadyMember    = "already_member"
	CodeInviteSelf       = "invite_self"
	CodePersonalList     = "personal_list"
	CodeUsernameInvalid  = "username_invalid"
	CodeUsernameTaken    = "username_taken"
	CodeUsernameReserved = "username_reserved"

	// What taking a todo or handing it back can lose to. The answer says
	// who has it now: see AssignmentConflict.
	CodeAlreadyClaimed   = "already_claimed"
	CodeNotAssignedToYou = "not_assigned_to_you"
	CodeTodoCompleted    = "todo_completed"

	// CodeTooManyLines refuses a capture of more lines than one request
	// may hold. The answer has limit and lines, and nothing was created.
	CodeTooManyLines = "too_many_lines"
)

// FieldError names one offending field of a validation failure.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Error is a non-2xx API response. Branch on Code; Message may change.
type Error struct {
	StatusCode int
	Code       string
	Message    string
	Fields     []FieldError
	RequestID  string
	RetryAfter time.Duration

	// body is the response as it came, for the calls whose errors carry
	// more than the fields above.
	body []byte
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Message != "" {
		b.WriteString(e.Message)
	} else {
		b.WriteString("request failed")
	}
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Code)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [req %s]", e.RequestID)
	}
	if len(e.Fields) > 0 {
		parts := make([]string, 0, len(e.Fields))
		for _, f := range e.Fields {
			parts = append(parts, fmt.Sprintf("%s: %s", f.Path, f.Message))
		}
		b.WriteString(" - " + strings.Join(parts, "; "))
	}
	return b.String()
}

// Retryable reports whether retrying the same request could succeed.
func (e *Error) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode == http.StatusBadGateway ||
		e.StatusCode == http.StatusServiceUnavailable ||
		e.StatusCode >= 500
}

// CodeOf returns the API error code for err, or "" if err is not an
// *Error.
func CodeOf(err error) string {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// ScopeAccount is the scope that export and account deletion need. Only
// browser sessions and first-party apps hold it.
const ScopeAccount = "account"

// HasScope reports whether a space-separated scope string, as sign-in
// returns it, includes want.
func HasScope(scope, want string) bool {
	for _, s := range strings.Fields(scope) {
		if s == want {
			return true
		}
	}
	return false
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// Client talks to the Evado API with a bearer token.
type Client struct {
	baseURL string
	http    *http.Client
	agent   string

	// The TUI runs requests in parallel on one client, so what they can
	// change afterwards is guarded. The fields above are fixed by New.
	mu     sync.RWMutex
	token  string
	lastID string
}

// Option customises a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (for a test server or tests).
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = MigrateLegacy(strings.TrimRight(u, "/"))
		}
	}
}

// WithHTTPClient supplies the underlying HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.agent = ua }
}

// New builds a client for the given bearer token. A token starting with
// "evt_" is stripped of a stray "Bearer " prefix for convenience.
func New(token string, opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		token:   strings.TrimPrefix(strings.TrimSpace(token), "Bearer "),
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Follow nothing: a redirect would drop the token and turn a
			// moved server into a baffling "token rejected". do reports it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		agent: "evado-tui",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// HasToken reports whether a bearer token is set.
func (c *Client) HasToken() bool { return c.bearer() != "" }

func (c *Client) bearer() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// BaseURL is the API root in use.
func (c *Client) BaseURL() string { return c.baseURL }

// SetToken swaps the bearer token, e.g. after onboarding.
func (c *Client) SetToken(token string) {
	token = strings.TrimPrefix(strings.TrimSpace(token), "Bearer ")
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// LastRequestID is the X-Request-Id of the most recent response, handy
// when reporting a problem upstream.
func (c *Client) LastRequestID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastID
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			rdr = bytes.NewReader(raw)
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				return 0, fmt.Errorf("encode request: %w", err)
			}
			rdr = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.agent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := c.bearer(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	// Reading the body to EOF is what lets the connection be reused; the
	// close error carries nothing useful once the body is drained.
	defer func() { _ = res.Body.Close() }()
	c.mu.Lock()
	c.lastID = res.Header.Get("X-Request-Id")
	c.mu.Unlock()

	raw, err := ReadResponse(res.Body)
	if err != nil {
		return res.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return res.StatusCode, redirectError(res)
	}
	if res.StatusCode >= 400 {
		return res.StatusCode, newError(res, raw)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return res.StatusCode, nil
}

// MaxResponseBytes is the most read of one answer from a server. The
// largest real ones are an account export and a sync page of 200 todos
// with their notes and checklists, which stay well under it; four times
// what an import file may be leaves room for an export that grew past
// that. A server sending more is refused rather than read until memory
// runs out.
const MaxResponseBytes = 4 * MaxImportBytes

// ErrResponseTooLarge is a server answer longer than MaxResponseBytes.
var ErrResponseTooLarge = fmt.Errorf("the server sent more than %d MiB", MaxResponseBytes>>20)

// ReadResponse reads a response body, refusing one longer than
// MaxResponseBytes.
func ReadResponse(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	return raw, nil
}

// Call makes a request the typed methods do not cover, such as a /sync
// body that has to tell a cleared field from an untouched one.
func (c *Client) Call(ctx context.Context, method, path string, body, out any) error {
	_, err := c.do(ctx, method, path, body, out)
	return err
}

// MigrateLegacy points a saved or typed legacy production address at the
// current one, keeping the scheme-relative path.
func MigrateLegacy(base string) string {
	u, err := url.Parse(base)
	if err != nil || !strings.EqualFold(u.Hostname(), legacyHost) {
		return base
	}
	u.Scheme = "https"
	u.Host = "evado.app"
	return u.String()
}

func redirectError(res *http.Response) *Error {
	e := &Error{
		StatusCode: res.StatusCode,
		Code:       CodeRedirected,
		RequestID:  res.Header.Get("X-Request-Id"),
		Message:    "the server moved",
	}
	if loc := res.Header.Get("Location"); loc != "" {
		e.Message = "the server moved to " + loc + "; point the server setting at the new host"
	}
	return e
}

func newError(res *http.Response, raw []byte) *Error {
	e := &Error{
		StatusCode: res.StatusCode,
		Code:       strings.ToLower(strings.ReplaceAll(http.StatusText(res.StatusCode), " ", "_")),
		Message:    http.StatusText(res.StatusCode),
		RequestID:  res.Header.Get("X-Request-Id"),
		body:       raw,
	}
	if secs, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil {
		e.RetryAfter = time.Duration(secs) * time.Second
	}
	var body struct {
		Error  string       `json:"error"`
		Code   string       `json:"code"`
		Fields []FieldError `json:"fields"`
		// OAuth errors use RFC 6749 naming.
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(raw, &body) == nil {
		if body.Error != "" {
			e.Message = body.Error
		}
		if body.ErrorDescription != "" {
			e.Message = body.ErrorDescription
		}
		e.Code = body.Code
		e.Fields = body.Fields
	}
	return e
}

// -- todos ------------------------------------------------------------------

// ListTodos returns the first page of the todos matching f, or the change
// feed when f.Since is set. It does not say whether more match; use
// ListTodosPage to go past the first page.
func (c *Client) ListTodos(ctx context.Context, f TodoFilter) ([]Todo, error) {
	p, err := c.ListTodosPage(ctx, f)
	return p.Todos, err
}

// ListTodosWithSteps is ListTodos plus the checklists the answer carried,
// by todo id. The service's list leaves them out, so the map is empty
// there; the todos kept on this computer send them with every todo, and a
// todo with no steps maps to an empty slice, which is different from a
// todo whose steps are not known.
func (c *Client) ListTodosWithSteps(ctx context.Context, f TodoFilter) ([]Todo, map[string][]ChecklistItem, error) {
	p, err := c.ListTodosPage(ctx, f)
	return p.Todos, p.Steps, err
}

// ListTodosPage returns one page of the todos matching f, with its
// checklists as ListTodosWithSteps does. When NextCursor is set there are
// more: ask again with it as f.Cursor and the same filters.
func (c *Client) ListTodosPage(ctx context.Context, f TodoFilter) (TodoPage, error) {
	q := url.Values{}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.ListID != "" {
		q.Set("listId", f.ListID)
	}
	if f.Assigned != "" {
		q.Set("assigned", f.Assigned)
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Undated {
		q.Set("undated", "1")
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Cursor != "" {
		q.Set("cursor", f.Cursor)
	}
	if f.Since != "" {
		q.Set("since", f.Since)
	}
	var out struct {
		Todos      []SyncTodo `json:"todos"`
		NextCursor *string    `json:"nextCursor"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/todos?"+q.Encode(), nil, &out); err != nil {
		return TodoPage{}, err
	}
	page := TodoPage{Todos: make([]Todo, len(out.Todos))}
	if out.NextCursor != nil {
		page.NextCursor = *out.NextCursor
	}
	for i, t := range out.Todos {
		page.Todos[i] = t.Todo
		if t.Checklist != nil {
			if page.Steps == nil {
				page.Steps = map[string][]ChecklistItem{}
			}
			page.Steps[t.ID] = t.Checklist
		}
	}
	return page, nil
}

// ChangeFeed pulls the changes after a cursor an earlier sync returned.
// When hasMore is true, call again with the returned cursor. The first
// cursor comes from Sync with no Since: the feed cannot start without one.
func (c *Client) ChangeFeed(ctx context.Context, since string) (SyncResponse, error) {
	var out SyncResponse
	q := url.Values{}
	if since != "" {
		q.Set("since", since)
	}
	_, err := c.do(ctx, http.MethodGet, "/todos?"+q.Encode(), nil, &out)
	return out, err
}

// GetTodo fetches one todo.
func (c *Client) GetTodo(ctx context.Context, id string) (Todo, error) {
	var out struct {
		Todo Todo `json:"todo"`
	}
	_, err := c.do(ctx, http.MethodGet, "/todos/"+url.PathEscape(id), nil, &out)
	return out.Todo, err
}

// CreateTodos creates one or more todos. Newlines in n.Title split the
// input server-side, so a single call can yield several todos. When n.ID
// is set, re-posting it is idempotent instead of duplicating.
func (c *Client) CreateTodos(ctx context.Context, n NewTodo) ([]Todo, error) {
	var out struct {
		Todos []Todo `json:"todos"`
	}
	_, err := c.do(ctx, http.MethodPost, "/todos", n, &out)
	return out.Todos, err
}

// UpdateTodo applies p to a todo. Completing a recurring todo returns the
// next occurrence, if any.
func (c *Client) UpdateTodo(ctx context.Context, id string, p TodoPatch) (Todo, *Todo, error) {
	var out struct {
		Todo Todo  `json:"todo"`
		Next *Todo `json:"next"`
	}
	_, err := c.do(ctx, http.MethodPatch, "/todos/"+url.PathEscape(id), p, &out)
	if err != nil {
		return Todo{}, nil, err
	}
	return out.Todo, out.Next, nil
}

// DeleteTodo soft-deletes a todo; syncers see a tombstone.
func (c *Client) DeleteTodo(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/todos/"+url.PathEscape(id), nil, nil)
	return err
}

// CompleteTodo marks a todo done. A recurring todo materialises its next
// occurrence, returned as the second value.
func (c *Client) CompleteTodo(ctx context.Context, id string) (Todo, *Todo, error) {
	var out struct {
		Todo Todo  `json:"todo"`
		Next *Todo `json:"next"`
	}
	_, err := c.do(ctx, http.MethodPost, "/todos/"+url.PathEscape(id)+"/complete", nil, &out)
	if err != nil {
		return Todo{}, nil, err
	}
	return out.Todo, out.Next, nil
}

// UncompleteTodo reopens a todo.
func (c *Client) UncompleteTodo(ctx context.Context, id string) (Todo, error) {
	var out struct {
		Todo Todo `json:"todo"`
	}
	_, err := c.do(ctx, http.MethodPost, "/todos/"+url.PathEscape(id)+"/uncomplete", nil, &out)
	return out.Todo, err
}

// ClaimTodo takes a todo in a shared list for the caller ("I'll do it"),
// only while it is still Anyone's. Of competing claims one wins, and the
// others get an *AssignmentConflict naming the winner, so a colleague's
// claim is never overwritten the way setting assigneeId would. Claiming a
// todo that is already the caller's returns it unchanged.
func (c *Client) ClaimTodo(ctx context.Context, id string) (Todo, error) {
	return c.assignment(ctx, id, "claim")
}

// ReleaseTodo hands the caller's todo back to Anyone, only while it is
// still theirs. After someone reassigned it the answer is an
// *AssignmentConflict and nothing changes. Releasing a todo that is
// already Anyone's returns it unchanged.
func (c *Client) ReleaseTodo(ctx context.Context, id string) (Todo, error) {
	return c.assignment(ctx, id, "release")
}

func (c *Client) assignment(ctx context.Context, id, verb string) (Todo, error) {
	var out struct {
		Todo Todo `json:"todo"`
	}
	_, err := c.do(ctx, http.MethodPost, "/todos/"+url.PathEscape(id)+"/"+verb, nil, &out)
	if err != nil {
		return Todo{}, asConflict(err)
	}
	return out.Todo, nil
}

// AssignmentConflict is a claim or release that lost to another change.
// Todo is the todo as it is now, and Assignee who has it, nil when nobody
// does or it is someone who has left the list. The service sends no todo
// for todo_completed, so Todo can be nil too.
type AssignmentConflict struct {
	Err      *Error
	Todo     *Todo
	Assignee *Member
}

func (e *AssignmentConflict) Error() string { return e.Err.Error() }

// Unwrap gives the API error, so CodeOf and exit codes see the 409.
func (e *AssignmentConflict) Unwrap() error { return e.Err }

// Holder is what to call whoever has the todo, or "" when that is nobody
// the list knows.
func (e *AssignmentConflict) Holder() string {
	if e.Assignee == nil {
		return ""
	}
	return e.Assignee.Name()
}

// asConflict reads the todo and assignee out of a 409, and leaves every
// other error as it was.
func asConflict(err error) error {
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
		return err
	}
	var body struct {
		Todo     *Todo   `json:"todo"`
		Assignee *Member `json:"assignee"`
	}
	_ = json.Unmarshal(apiErr.body, &body)
	return &AssignmentConflict{Err: apiErr, Todo: body.Todo, Assignee: body.Assignee}
}

// Parse previews natural-language date parsing without creating a todo.
func (c *Client) Parse(ctx context.Context, title string) (ParsePreview, error) {
	return c.ParseWithSmart(ctx, title, nil)
}

// ParseWithSmart can bypass the account's decision model for one title.
func (c *Client) ParseWithSmart(ctx context.Context, title string, smart *bool) (ParsePreview, error) {
	// The service wraps the result in "parsed". The bare form is read too,
	// for a server that answers without the wrapper.
	var out struct {
		ParsePreview
		Parsed *ParsePreview `json:"parsed"`
	}
	body := struct {
		Title string `json:"title"`
		Smart *bool  `json:"smart,omitempty"`
	}{Title: title, Smart: smart}
	_, err := c.do(ctx, http.MethodPost, "/parse", body, &out)
	if err != nil {
		return out.ParsePreview, err
	}
	if out.Parsed != nil {
		out.ParsePreview = *out.Parsed
	}
	if out.Title == "" {
		out.Title = title
	}
	return out.ParsePreview, nil
}

// -- checklist --------------------------------------------------------------

// ListChecklist returns a todo's steps in order.
func (c *Client) ListChecklist(ctx context.Context, todoID string) ([]ChecklistItem, error) {
	var out struct {
		Items []ChecklistItem `json:"items"`
	}
	_, err := c.do(ctx, http.MethodGet, "/todos/"+url.PathEscape(todoID)+"/checklist", nil, &out)
	return out.Items, err
}

// AddChecklistItem appends a step to a todo.
func (c *Client) AddChecklistItem(ctx context.Context, todoID, title string) (ChecklistItem, error) {
	var out struct {
		Item ChecklistItem `json:"item"`
	}
	body := struct {
		Title string `json:"title"`
	}{Title: title}
	_, err := c.do(ctx, http.MethodPost, "/todos/"+url.PathEscape(todoID)+"/checklist", body, &out)
	return out.Item, err
}

// UpdateChecklistItem renames or ticks (completedAt) a step. A nil
// completedAt clears the tick.
func (c *Client) UpdateChecklistItem(ctx context.Context, id string, title *string, completedAt *time.Time) (ChecklistItem, error) {
	var out struct {
		Item ChecklistItem `json:"item"`
	}
	// Unticking has to say so: leaving completedAt out means "leave it
	// alone" to the service, so a rename sends only the title and a tick
	// or untick always sends completedAt, as a time or as null.
	body := map[string]any{}
	if title != nil {
		body["title"] = *title
	}
	if title == nil || completedAt != nil {
		body["completedAt"] = completedAt
	}
	_, err := c.do(ctx, http.MethodPatch, "/checklist/"+url.PathEscape(id), body, &out)
	return out.Item, err
}

// ToggleChecklistItem ticks an unticked step and vice versa.
func (c *Client) ToggleChecklistItem(ctx context.Context, item ChecklistItem) (ChecklistItem, error) {
	var at *time.Time
	if item.Completed() {
		at = nil
	} else {
		now := time.Now().UTC()
		at = &now
	}
	return c.UpdateChecklistItem(ctx, item.ID, nil, at)
}

// DeleteChecklistItem removes a step.
func (c *Client) DeleteChecklistItem(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/checklist/"+url.PathEscape(id), nil, nil)
	return err
}

// -- lists ------------------------------------------------------------------

// ListLists returns every list visible to the account.
func (c *Client) ListLists(ctx context.Context) ([]List, error) {
	var out struct {
		Lists []List `json:"lists"`
	}
	_, err := c.do(ctx, http.MethodGet, "/lists", nil, &out)
	return out.Lists, err
}

// CreateList creates a list.
func (c *Client) CreateList(ctx context.Context, name string) (List, error) {
	var out struct {
		List List `json:"list"`
	}
	body := struct {
		Name string `json:"name"`
	}{Name: name}
	_, err := c.do(ctx, http.MethodPost, "/lists", body, &out)
	return out.List, err
}

// RenameList renames a list. Owners only.
func (c *Client) RenameList(ctx context.Context, id, name string) (List, error) {
	var out struct {
		List List `json:"list"`
	}
	body := struct {
		Name string `json:"name"`
	}{Name: name}
	_, err := c.do(ctx, http.MethodPatch, "/lists/"+url.PathEscape(id), body, &out)
	return out.List, err
}

// DeleteList permanently deletes a list and its todos for everyone. The
// personal list cannot be deleted.
func (c *Client) DeleteList(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/lists/"+url.PathEscape(id), nil, nil)
	return err
}

// ListMembers returns a list's members; emails are masked except yours.
func (c *Client) ListMembers(ctx context.Context, listID string) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	_, err := c.do(ctx, http.MethodGet, "/lists/"+url.PathEscape(listID)+"/members", nil, &out)
	return out.Members, err
}

// InviteMember invites an existing Evado account to a list. Nobody joins
// until they accept. Asking again for the same person answers the same
// success and sends nothing, even after a decline, so a decline is never
// revealed: callers must not word the answer as if it were.
func (c *Client) InviteMember(ctx context.Context, listID string, who InviteTarget) (Invitee, error) {
	set := 0
	for _, s := range []string{who.UserID, who.Username, who.Email} {
		if strings.TrimSpace(s) != "" {
			set++
		}
	}
	if set != 1 {
		return Invitee{}, errors.New("name exactly one of a user id, a username or an email to invite")
	}
	var out struct {
		Invitee Invitee `json:"invitee"`
	}
	_, err := c.do(ctx, http.MethodPost, "/lists/"+url.PathEscape(listID)+"/invitations", who, &out)
	return out.Invitee, err
}

// People returns everyone the account shares a list with, for picking
// someone to invite to another list.
func (c *Client) People(ctx context.Context) ([]Person, error) {
	var out struct {
		People []Person `json:"people"`
	}
	_, err := c.do(ctx, http.MethodGet, "/people", nil, &out)
	return out.People, err
}

// RemoveMember removes a member. Owners may remove anyone; members may
// remove themselves.
func (c *Client) RemoveMember(ctx context.Context, listID, userID string) error {
	p := "/lists/" + url.PathEscape(listID) + "/members/" + url.PathEscape(userID)
	_, err := c.do(ctx, http.MethodDelete, p, nil, nil)
	return err
}

// CreateInvite mints a 7-day invite link. The URL is shown exactly once.
func (c *Client) CreateInvite(ctx context.Context, listID string) (Invite, error) {
	var out Invite
	_, err := c.do(ctx, http.MethodPost, "/lists/"+url.PathEscape(listID)+"/invites", nil, &out)
	return out, err
}

// JoinList joins a list with an invite code or the code from an invite URL.
func (c *Client) JoinList(ctx context.Context, code string) (List, error) {
	var out struct {
		List List `json:"list"`
	}
	body := struct {
		Code string `json:"code"`
	}{Code: code}
	_, err := c.do(ctx, http.MethodPost, "/join", body, &out)
	return out.List, err
}

// -- invitations ------------------------------------------------------------

// Invitations returns the pending invitations addressed to the account,
// newest first.
func (c *Client) Invitations(ctx context.Context) (InvitationList, error) {
	var out InvitationList
	_, err := c.do(ctx, http.MethodGet, "/invitations", nil, &out)
	return out, err
}

// AcceptInvitation joins the list. Its todos arrive on the next sync pull.
func (c *Client) AcceptInvitation(ctx context.Context, id string) (List, error) {
	var out struct {
		List List `json:"list"`
	}
	_, err := c.do(ctx, http.MethodPost, "/invitations/"+url.PathEscape(id)+"/accept", nil, &out)
	return out.List, err
}

// DeclineInvitation turns the invitation down without telling the inviter.
func (c *Client) DeclineInvitation(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/invitations/"+url.PathEscape(id)+"/decline", nil, nil)
	return err
}

// RestoreInvitation takes back a decline made in the last seven days, and
// makes the invitation pending again.
func (c *Client) RestoreInvitation(ctx context.Context, id string) (Invitation, error) {
	var out struct {
		Invitation Invitation `json:"invitation"`
	}
	_, err := c.do(ctx, http.MethodPost, "/invitations/"+url.PathEscape(id)+"/restore", nil, &out)
	return out.Invitation, err
}

// -- sync -------------------------------------------------------------------

// Sync pushes local changes and pulls remote ones in one round trip. Pass
// the previous cursor as req.Since; an empty Since runs an initial sync.
// While resp.HasMore is true, call again with Since = resp.Cursor and no
// changes.
func (c *Client) Sync(ctx context.Context, req SyncRequest) (SyncResponse, error) {
	var out SyncResponse
	_, err := c.do(ctx, http.MethodPost, "/sync", req, &out)
	return out, err
}

// -- account ----------------------------------------------------------------

// Me returns the current user and their lists.
func (c *Client) Me(ctx context.Context) (User, []List, error) {
	var out struct {
		User                  User   `json:"user"`
		SmartParsingAvailable bool   `json:"smartParsingAvailable"`
		Lists                 []List `json:"lists"`
	}
	_, err := c.do(ctx, http.MethodGet, "/me", nil, &out)
	out.User.SmartParsingAvailable = out.SmartParsingAvailable
	return out.User, out.Lists, err
}

// UpdateProfile changes the timezone and/or the username. Only the fields
// set in p are sent, so editing one never disturbs the other. If the
// service refuses the username nothing changes.
func (c *Client) UpdateProfile(ctx context.Context, p ProfilePatch) (User, error) {
	if p.TZ == nil && p.Username == nil && p.SmartParsing == nil {
		return User{}, errors.New("nothing to update: send a timezone, username or smartParsing")
	}
	var out struct {
		User User `json:"user"`
	}
	_, err := c.do(ctx, http.MethodPatch, "/me", p, &out)
	return out.User, err
}

// SetTimezone updates the account timezone. Date parsing is done in it.
func (c *Client) SetTimezone(ctx context.Context, tz string) (User, error) {
	return c.UpdateProfile(ctx, ProfilePatch{TZ: Val(tz)})
}

// SetUsername sets the username people find the account by, or clears it
// when username is nil. The service normalises it and tells which rule a
// refused one broke.
func (c *Client) SetUsername(ctx context.Context, username *string) (User, error) {
	if username == nil {
		return c.UpdateProfile(ctx, ProfilePatch{Username: Null})
	}
	// Typed text can hold anything, so it is encoded rather than quoted.
	raw, err := json.Marshal(*username)
	if err != nil {
		return User{}, err
	}
	return c.UpdateProfile(ctx, ProfilePatch{Username: raw})
}

// Export downloads everything the account can see. It needs the account
// scope: pasted API tokens get 403.
func (c *Client) Export(ctx context.Context) (Export, error) {
	var out Export
	_, err := c.do(ctx, http.MethodGet, "/me/export", nil, &out)
	return out, err
}

// DeleteAccount permanently deletes the account. confirmEmail must repeat
// the account email. Shared lists it owns become read-only for their
// members. Needs the account scope.
func (c *Client) DeleteAccount(ctx context.Context, confirmEmail string) (int, error) {
	body := struct {
		ConfirmEmail string `json:"confirmEmail"`
	}{ConfirmEmail: confirmEmail}
	var out struct {
		Deleted     bool `json:"deleted"`
		FrozenLists int  `json:"frozenLists"`
	}
	_, err := c.do(ctx, http.MethodDelete, "/me", body, &out)
	return out.FrozenLists, err
}

// -- session-only endpoints -------------------------------------------------
// Token and notification-channel management reject token auth with 403
// (session_required). They are implemented so the TUI can show the real
// error, and so they light up unchanged if a session is ever added.

// ListTokens lists active API tokens. Session only.
func (c *Client) ListTokens(ctx context.Context) ([]APIToken, error) {
	var out struct {
		Tokens []APIToken `json:"tokens"`
	}
	_, err := c.do(ctx, http.MethodGet, "/tokens", nil, &out)
	return out.Tokens, err
}

// CreateToken mints a token; the value is shown exactly once. Session only.
func (c *Client) CreateToken(ctx context.Context, name string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	body := struct {
		Name string `json:"name"`
	}{Name: name}
	_, err := c.do(ctx, http.MethodPost, "/tokens", body, &out)
	return out.Token, err
}

// RevokeToken revokes a token. Session only.
func (c *Client) RevokeToken(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/tokens/"+url.PathEscape(id), nil, nil)
	return err
}

// ListChannels lists reminder channels; configs are never returned. Session only.
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	var out struct {
		Channels []Channel `json:"channels"`
	}
	_, err := c.do(ctx, http.MethodGet, "/notification-channels", nil, &out)
	return out.Channels, err
}

// CreateChannel adds a reminder channel. Session only.
func (c *Client) CreateChannel(ctx context.Context, kind, label string, cfg map[string]any) (Channel, error) {
	var out struct {
		Channel Channel `json:"channel"`
	}
	body := struct {
		Type   string         `json:"type"`
		Label  string         `json:"label"`
		Config map[string]any `json:"config"`
	}{Type: kind, Label: label, Config: cfg}
	_, err := c.do(ctx, http.MethodPost, "/notification-channels", body, &out)
	return out.Channel, err
}

// UpdateChannel updates a channel's label, enabled flag, or config. Session only.
func (c *Client) UpdateChannel(ctx context.Context, id string, body map[string]any) (Channel, error) {
	var out struct {
		Channel Channel `json:"channel"`
	}
	_, err := c.do(ctx, http.MethodPatch, "/notification-channels/"+url.PathEscape(id), body, &out)
	return out.Channel, err
}

// DeleteChannel removes a reminder channel. Session only.
func (c *Client) DeleteChannel(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/notification-channels/"+url.PathEscape(id), nil, nil)
	return err
}

// TestChannel sends a test notification. Session only.
func (c *Client) TestChannel(ctx context.Context, id string) error {
	p := "/notification-channels/" + url.PathEscape(id) + "/test"
	_, err := c.do(ctx, http.MethodPost, p, nil, nil)
	return err
}
