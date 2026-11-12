package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "evt_test_token"

// newServer returns a test server plus a client pointed at it.
func newServer(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(testToken, WithBaseURL(srv.URL)), srv
}

func TestAuthHeaderAndAccept(t *testing.T) {
	var gotAuth, gotAccept, gotAgent string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotAgent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))

	if _, err := c.ListTodos(context.Background(), TodoFilter{Status: StatusOpen}); err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if gotAuth != "Bearer "+testToken {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotAgent == "" {
		t.Error("missing User-Agent")
	}
}

func TestTokenPrefixStripped(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))
	defer srv.Close()

	for _, in := range []string{"evt_abc", "Bearer evt_abc", "  evt_abc  "} {
		c := New(in, WithBaseURL(srv.URL))
		if _, err := c.ListTodos(context.Background(), TodoFilter{}); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != "Bearer evt_abc" {
			t.Errorf("input %q sent %q", in, got)
		}
	}
}

func TestListTodosQueryParams(t *testing.T) {
	var query url.Values
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"todos":[{"id":"t1","listId":"l1","title":"x"}]}`))
	}))

	todos, err := c.ListTodos(context.Background(), TodoFilter{
		Status:   StatusToday,
		ListID:   "l1",
		Assigned: "me",
		Query:    "cable bill",
		Undated:  true,
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(todos) != 1 || todos[0].ID != "t1" {
		t.Fatalf("todos = %+v", todos)
	}
	checks := map[string]string{
		"status": "today", "listId": "l1", "assigned": "me",
		"q": "cable bill", "undated": "1", "limit": "50",
	}
	for k, want := range checks {
		if got := query.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestChangeFeedPassesSince(t *testing.T) {
	var got string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("since")
		_, _ = w.Write([]byte(`{"todos":[],"lists":[],"cursor":"4121","hasMore":true}`))
	}))

	resp, err := c.ChangeFeed(context.Background(), "4120")
	if err != nil {
		t.Fatalf("ChangeFeed: %v", err)
	}
	if got != "4120" {
		t.Errorf("since = %q, want the cursor sent back as it was", got)
	}
	if resp.Cursor != "4121" {
		t.Errorf("cursor = %q", resp.Cursor)
	}
	if !resp.HasMore {
		t.Error("hasMore not decoded")
	}
}

func TestCreateTodosPostsBody(t *testing.T) {
	var body NewTodo
	var method string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"todos":[{"id":"t1","listId":"l1","title":"Pay rent Friday"}]}`))
	}))

	todos, err := c.CreateTodos(context.Background(), NewTodo{
		Title:  "Pay rent Friday",
		ListID: "l1",
	})
	if err != nil {
		t.Fatalf("CreateTodos: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %s", method)
	}
	if body.Title != "Pay rent Friday" || body.ListID != "l1" {
		t.Errorf("body = %+v", body)
	}
	if len(todos) != 1 {
		t.Fatalf("got %d todos", len(todos))
	}
}

func TestCreateTodosOmitsEmptyFields(t *testing.T) {
	raw := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw <- string(b)
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))
	defer srv.Close()

	c := New(testToken, WithBaseURL(srv.URL))
	// A nil notes pointer must not appear in the payload, otherwise the
	// server would clear a field the user never touched.
	if _, err := c.CreateTodos(context.Background(), NewTodo{Title: "hi"}); err != nil {
		t.Fatalf("CreateTodos: %v", err)
	}
	body := <-raw
	if strings.Contains(body, "notes") || strings.Contains(body, "listId") {
		t.Errorf("empty fields leaked into body: %s", body)
	}
}

func TestUpdateTodoReturnsNext(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var patch TodoPatch
		_ = json.NewDecoder(r.Body).Decode(&patch)
		if string(patch.IsCompleted) != "true" {
			t.Errorf("isCompleted = %q", patch.IsCompleted)
		}
		_, _ = w.Write([]byte(`{"todo":{"id":"t1","title":"a"},"next":{"id":"t2","title":"a"}}`))
	}))

	todo, next, err := c.UpdateTodo(context.Background(), "t1", TodoPatch{IsCompleted: BoolVal(true)})
	if err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	if todo.ID != "t1" {
		t.Errorf("todo = %+v", todo)
	}
	if next == nil || next.ID != "t2" {
		t.Errorf("next = %+v", next)
	}
}

func TestNullClearsField(t *testing.T) {
	raw := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw <- string(b)
		_, _ = w.Write([]byte(`{"todo":{"id":"t1"}}`))
	}))
	defer srv.Close()

	c := New(testToken, WithBaseURL(srv.URL))
	if _, _, err := c.UpdateTodo(context.Background(), "t1", TodoPatch{Notes: Null}); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	if body := <-raw; !strings.Contains(body, `"notes":null`) {
		t.Errorf("body = %s, want explicit null", body)
	}
}

func TestEmptyPatchDetected(t *testing.T) {
	if !(TodoPatch{}).Empty() {
		t.Error("zero patch should be empty")
	}
	if (TodoPatch{Title: Val("x")}).Empty() {
		t.Error("patch with a title should not be empty")
	}
}

func TestCompleteAndUncomplete(t *testing.T) {
	var paths []string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"todo":{"id":"t1"},"next":null}`))
	}))

	if _, _, err := c.CompleteTodo(context.Background(), "t1"); err != nil {
		t.Fatalf("CompleteTodo: %v", err)
	}
	if _, err := c.UncompleteTodo(context.Background(), "t1"); err != nil {
		t.Fatalf("UncompleteTodo: %v", err)
	}
	want := []string{
		"POST /todos/t1/complete",
		"POST /todos/t1/uncomplete",
	}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestChecklistRoundTrip(t *testing.T) {
	var seen []string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[{"id":"c1","title":"step","completedAt":"2026-01-01T00:00:00Z"}]}`))
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"item":{"id":"c2","title":"step"}}`))
		default:
			_, _ = w.Write([]byte(`{"item":{"id":"c1","title":"renamed"}}`))
		}
	}))

	items, err := c.ListChecklist(context.Background(), "t1")
	if err != nil || len(items) != 1 || !items[0].Completed() {
		t.Fatalf("ListChecklist = %+v, err %v", items, err)
	}
	if _, err := c.AddChecklistItem(context.Background(), "t1", "step"); err != nil {
		t.Fatalf("AddChecklistItem: %v", err)
	}
	toggled, err := c.ToggleChecklistItem(context.Background(), items[0])
	if err != nil {
		t.Fatalf("ToggleChecklistItem: %v", err)
	}
	if toggled.Completed() {
		t.Error("toggling a ticked step should clear it")
	}
	if err := c.DeleteChecklistItem(context.Background(), "c1"); err != nil {
		t.Fatalf("DeleteChecklistItem: %v", err)
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 calls, got %v", seen)
	}
}

func TestListsAndMembers(t *testing.T) {
	var seen []string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/lists":
			_, _ = w.Write([]byte(`{"lists":[{"id":"l1","name":"Home","isPersonal":false,"role":"owner","readOnly":false}]}`))
		case "/lists/l1/members":
			_, _ = w.Write([]byte(`{"members":[{"userId":"u1","email":"a@b.c","role":"owner"}]}`))
		case "/lists/l1/invites":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"inviteUrl":"https://x/abc","expiresIn":604800}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))

	lists, err := c.ListLists(context.Background())
	if err != nil || len(lists) != 1 || lists[0].Name != "Home" {
		t.Fatalf("ListLists = %+v, err %v", lists, err)
	}
	members, err := c.ListMembers(context.Background(), "l1")
	if err != nil || len(members) != 1 || members[0].UserID != "u1" {
		t.Fatalf("ListMembers = %+v, err %v", members, err)
	}
	inv, err := c.CreateInvite(context.Background(), "l1")
	if err != nil || inv.InviteURL == "" || inv.ExpiresIn != 604800 {
		t.Fatalf("CreateInvite = %+v, err %v", inv, err)
	}
	if err := c.RemoveMember(context.Background(), "l1", "u1"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

// invitationServer answers POST /lists/l1/invitations and records each
// body verbatim, so a test can tell what was sent from what was merely
// empty.
func invitationServer(t *testing.T, status int, reply string) (*Client, *[]map[string]json.RawMessage) {
	t.Helper()
	var bodies []map[string]json.RawMessage
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/lists/l1/invitations" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	return c, &bodies
}

func TestInviteMemberSendsExactlyOneIdentifier(t *testing.T) {
	c, bodies := invitationServer(t, http.StatusCreated, `{"ok":true,"invitee":{"userId":"u2","username":"sam"}}`)
	for _, who := range []InviteTarget{{Username: "@sam"}, {Email: "sam@example.com"}, {UserID: "u2"}} {
		got, err := c.InviteMember(context.Background(), "l1", who)
		if err != nil {
			t.Fatalf("InviteMember(%+v): %v", who, err)
		}
		if got.UserID != "u2" || got.Username == nil || *got.Username != "sam" {
			t.Errorf("invitee = %+v", got)
		}
	}
	want := []string{`{"username":"@sam"}`, `{"email":"sam@example.com"}`, `{"userId":"u2"}`}
	for i, body := range *bodies {
		if len(body) != 1 {
			t.Errorf("request %d sent %d fields, want exactly one: %v", i, len(body), body)
		}
		raw, _ := json.Marshal(body)
		if string(raw) != want[i] {
			t.Errorf("request %d body = %s, want %s", i, raw, want[i])
		}
	}
}

func TestInviteMemberRefusesAnythingButOneIdentifier(t *testing.T) {
	c, bodies := invitationServer(t, http.StatusCreated, `{"ok":true}`)
	for _, who := range []InviteTarget{
		{},
		{Username: "  "},
		{Username: "sam", Email: "sam@example.com"},
		{UserID: "u2", Username: "sam"},
		{UserID: "u2", Username: "sam", Email: "sam@example.com"},
	} {
		if _, err := c.InviteMember(context.Background(), "l1", who); err == nil {
			t.Errorf("InviteMember(%+v) succeeded, want an error", who)
		}
	}
	if len(*bodies) != 0 {
		t.Errorf("%d requests went out, want none", len(*bodies))
	}
}

func TestInviteMemberErrorCodes(t *testing.T) {
	for status, code := range map[int]string{
		http.StatusBadRequest:      CodePersonalList,
		http.StatusNotFound:        CodeUserNotFound,
		http.StatusConflict:        CodeAlreadyMember,
		http.StatusForbidden:       CodeListReadOnly,
		http.StatusTooManyRequests: CodeRateLimited,
	} {
		c, _ := invitationServer(t, status, `{"error":"no","code":"`+code+`"}`)
		_, err := c.InviteMember(context.Background(), "l1", InviteTarget{Username: "sam"})
		if got := CodeOf(err); got != code {
			t.Errorf("status %d: code = %q, want %q (%v)", status, got, code, err)
		}
	}
}

func TestPeople(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/people" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"people":[
			{"userId":"u2","username":"sam","email":"s***@example.com","sharedListIds":["l1"]},
			{"userId":"u3","username":null,"email":"j***@example.com","sharedListIds":[]}]}`))
	}))
	people, err := c.People(context.Background())
	if err != nil || len(people) != 2 {
		t.Fatalf("People = %+v, err %v", people, err)
	}
	if people[0].Name() != "@sam" || len(people[0].SharedListIDs) != 1 {
		t.Errorf("first person = %+v", people[0])
	}
	if people[1].Username != nil || people[1].Name() != "j***@example.com" {
		t.Errorf("a null username must fall back to the masked email: %+v", people[1])
	}
}

func TestUsernamesAreNullable(t *testing.T) {
	var u struct {
		User User     `json:"user"`
		Mems []Member `json:"members"`
	}
	raw := `{"user":{"id":"u1","email":"a@b.c","username":null,"tz":"UTC"},
		"members":[{"userId":"u1","username":"ann","email":"a@b.c","role":"owner"},
		{"userId":"u2","username":null,"email":"s***@b.c","role":"member"},
		{"userId":"u3","email":"t***@b.c","role":"member"}]}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if u.User.Username != nil || Handle(u.User.Username) != "" {
		t.Errorf("a null username decoded as %v", u.User.Username)
	}
	if got := []string{u.Mems[0].Name(), u.Mems[1].Name(), u.Mems[2].Name()}; got[0] != "@ann" || got[1] != "s***@b.c" || got[2] != "t***@b.c" {
		t.Errorf("member names = %v, want the username or else the returned email", got)
	}
	// A cleared username has to go out as null, not vanish from the body.
	out, _ := json.Marshal(User{ID: "u1"})
	if !strings.Contains(string(out), `"username":null`) {
		t.Errorf("user encodes as %s, want an explicit null username", out)
	}
}

func TestErrorMapping(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_123")
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Too many requests","code":"rate_limited"}`))
	}))

	_, err := c.ListTodos(context.Background(), TodoFilter{})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if apiErr.Code != CodeRateLimited {
		t.Errorf("Code = %q", apiErr.Code)
	}
	if apiErr.Message != "Too many requests" {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if apiErr.RetryAfter != 12*time.Second {
		t.Errorf("RetryAfter = %v", apiErr.RetryAfter)
	}
	if apiErr.RequestID != "req_123" {
		t.Errorf("RequestID = %q", apiErr.RequestID)
	}
	if !apiErr.Retryable() {
		t.Error("429 should be retryable")
	}
	if CodeOf(err) != CodeRateLimited {
		t.Errorf("CodeOf = %q", CodeOf(err))
	}
	if !strings.Contains(apiErr.Error(), "req_123") {
		t.Errorf("Error() should include the request id: %s", apiErr)
	}
}

func TestErrorWithNoCodeKeepsTheStatusCode(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusBadRequest, `{"error":"Bad request"}`, CodeBadRequest},
		{http.StatusBadRequest, `{"error":"Bad request","code":""}`, CodeBadRequest},
		{http.StatusNotFound, `{}`, CodeNotFound},
		{http.StatusBadRequest, `{"error":"Bad request","code":"invalid_request"}`, CodeInvalidRequest},
		// A body that is not JSON gets the code the service uses for the
		// same status.
		{http.StatusTooManyRequests, `slow down`, CodeRateLimited},
		{http.StatusInternalServerError, `<html>oops</html>`, CodeInternal},
		{http.StatusBadGateway, `<html>bad gateway</html>`, CodeUpstreamFailed},
		{http.StatusServiceUnavailable, ``, CodeUpstreamFailed},
		{http.StatusConflict, ``, CodeConflict},
	}
	for _, tc := range cases {
		c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := c.GetTodo(context.Background(), "abc")
		if got := CodeOf(err); got != tc.want {
			t.Errorf("%d %s: code = %q, want %q", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 3, 10, 15, 0, 0, 500_000_000, time.UTC)
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"12", 12 * time.Second},
		{" 12 ", 12 * time.Second},
		{"0", 0},
		{"-5", 0},
		{"soon", 0},
		// An HTTP date is a wait until then, rounded up to whole seconds.
		{"Tue, 10 Mar 2026 15:01:30 GMT", 90 * time.Second},
		{"Tue, 10 Mar 2026 14:59:00 GMT", 0},
	}
	for _, tc := range cases {
		if got := retryAfter(tc.header, now); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}

	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "-30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Too many requests","code":"rate_limited"}`))
	}))
	_, err := c.ListTodos(context.Background(), TodoFilter{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 0 {
		t.Errorf("a negative Retry-After should be no wait: %+v", apiErr)
	}
}

func TestValidationErrorFields(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Bad request","code":"invalid_request","fields":[{"path":"title","message":"too long"}]}`))
	}))

	_, err := c.CreateTodos(context.Background(), NewTodo{Title: "x"})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if len(apiErr.Fields) != 1 || apiErr.Fields[0].Path != "title" {
		t.Fatalf("fields = %+v", apiErr.Fields)
	}
	if !strings.Contains(apiErr.Error(), "title") {
		t.Errorf("Error() should list fields: %s", apiErr)
	}
}

func TestSessionOnlyEndpointsSurfaceForbidden(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Session required","code":"session_required"}`))
	}))

	_, err := c.ListTokens(context.Background())
	if CodeOf(err) != CodeSessionRequired {
		t.Errorf("CodeOf = %q, want session_required", CodeOf(err))
	}
	_, err = c.ListChannels(context.Background())
	if CodeOf(err) != CodeSessionRequired {
		t.Errorf("CodeOf = %q, want session_required", CodeOf(err))
	}
	_, err = c.Export(context.Background())
	if CodeOf(err) != CodeSessionRequired {
		t.Errorf("CodeOf = %q, want session_required", CodeOf(err))
	}
}

func TestNotFound(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not found","code":"not_found"}`))
	}))
	_, err := c.GetTodo(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false for %v", err)
	}
}

func TestSyncRequestShape(t *testing.T) {
	var body SyncRequest
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"todos":[],"lists":[],"cursor":"7","hasMore":false,"errors":[{"id":"t9","error":"nope"}],"deferred":[{"id":"t10"},{"id":"t11"}]}`))
	}))

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	title := "from tui"
	done := true
	resp, err := c.Sync(context.Background(), SyncRequest{
		Since:          "6",
		AcceptDeferred: true,
		Changes: []SyncChange{{
			ID:          "t1",
			Title:       &title,
			IsCompleted: &done,
			Checklist:   []SyncStep{{ID: "c1", Title: "step"}},
			UpdatedAt:   since,
		}},
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(body.Changes) != 1 || len(body.Changes[0].Checklist) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.Since != "6" {
		t.Errorf("since = %v", body.Since)
	}
	if !body.AcceptDeferred {
		t.Error("acceptDeferred was not sent")
	}
	if len(resp.Deferred) != 2 || resp.Deferred[0].ID != "t10" || resp.Deferred[1].ID != "t11" {
		t.Errorf("deferred = %+v", resp.Deferred)
	}
	if len(resp.Errors) != 1 || resp.Errors[0].ID != "t9" {
		t.Errorf("errors = %+v", resp.Errors)
	}
}

func TestSetTimezoneAndDeleteAccount(t *testing.T) {
	var tz string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			_ = json.NewDecoder(r.Body).Decode(&struct{ TZ *string }{TZ: &tz})
			_, _ = w.Write([]byte(`{"user":{"id":"u1","email":"a@b.c","tz":"Europe/Madrid"}}`))
		default:
			_, _ = w.Write([]byte(`{"deleted":true,"frozenLists":2}`))
		}
	}))

	u, err := c.SetTimezone(context.Background(), "Europe/Madrid")
	if err != nil {
		t.Fatalf("SetTimezone: %v", err)
	}
	if u.TZ != "Europe/Madrid" || tz != "Europe/Madrid" {
		t.Errorf("user = %+v sent tz = %q", u, tz)
	}
	frozen, err := c.DeleteAccount(context.Background(), "a@b.c")
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if frozen != 2 {
		t.Errorf("frozenLists = %d", frozen)
	}
}

func TestSetUsernameLeavesTheTimezoneAlone(t *testing.T) {
	var bodies []string
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/me" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		_, _ = w.Write([]byte(`{"user":{"id":"u1","email":"a@b.c","username":"sam","tz":"Europe/Madrid"}}`))
	}))
	name := "@Sam"
	u, err := c.SetUsername(context.Background(), &name)
	if err != nil || u.Username == nil || *u.Username != "sam" || u.TZ != "Europe/Madrid" {
		t.Fatalf("SetUsername = %+v, err %v", u, err)
	}
	if _, err := c.SetUsername(context.Background(), nil); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if _, err := c.SetTimezone(context.Background(), "UTC"); err != nil {
		t.Fatalf("SetTimezone: %v", err)
	}
	want := []string{`{"username":"@Sam"}`, `{"username":null}`, `{"tz":"UTC"}`}
	if strings.Join(bodies, "|") != strings.Join(want, "|") {
		t.Errorf("bodies = %v, want %v", bodies, want)
	}
	if _, err := c.UpdateProfile(context.Background(), ProfilePatch{}); err == nil {
		t.Error("an empty profile patch should not be sent")
	}
}

func TestUsernameErrors(t *testing.T) {
	for status, code := range map[int]string{
		http.StatusBadRequest: CodeUsernameInvalid,
		http.StatusConflict:   CodeUsernameTaken,
	} {
		c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"That username is not available","code":"` + code + `"}`))
		}))
		name := "sam"
		_, err := c.SetUsername(context.Background(), &name)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != code || apiErr.Message != "That username is not available" {
			t.Errorf("status %d: err = %v, want code %q with the server's words", status, err, code)
		}
	}
}

func TestPathEscaping(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(testToken, WithBaseURL(srv.URL))
	// A list id with a slash must not escape its path segment.
	_ = c.DeleteTodo(context.Background(), "a/b")
	if strings.Count(path, "/") != 2 {
		t.Errorf("path = %q, want the id escaped into one segment", path)
	}
}

func TestLastRequestID(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_abc")
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))
	_, _ = c.ListTodos(context.Background(), TodoFilter{})
	if c.LastRequestID() != "req_abc" {
		t.Errorf("LastRequestID = %q", c.LastRequestID())
	}
}

// The TUI batches requests on one client, so request metadata and token
// replacement must not race. Run under -race to see it fail without the lock.
func TestSharedClientConcurrentUse(t *testing.T) {
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_shared")
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				if _, err := c.ListTodos(context.Background(), TodoFilter{}); err != nil {
					t.Errorf("ListTodos: %v", err)
					return
				}
				_ = c.LastRequestID()
				_ = c.HasToken()
				if i%4 == 0 {
					c.SetToken(testToken)
				}
			}
		}()
	}
	wg.Wait()

	if got := c.LastRequestID(); got != "req_shared" {
		t.Errorf("LastRequestID = %q", got)
	}
}

func TestBaseURLAndTokenAccessors(t *testing.T) {
	c := New("")
	if c.HasToken() {
		t.Error("empty token should report false")
	}
	c.SetToken("evt_new")
	if !c.HasToken() || c.BaseURL() != DefaultBaseURL {
		t.Errorf("HasToken = %v, BaseURL = %q", c.HasToken(), c.BaseURL())
	}
	if c := New("t", WithBaseURL("http://x/api/v1/")); c.BaseURL() != "http://x/api/v1" {
		t.Errorf("trailing slash not trimmed: %q", c.BaseURL())
	}
}

func TestTodoHelpers(t *testing.T) {
	now := time.Now()
	todo := Todo{DeletedAt: &now}
	if !todo.Tombstoned() {
		t.Error("DeletedAt should mean tombstoned")
	}
	if todo.Completed() {
		t.Error("zero IsCompleted should be false")
	}
	if !(Todo{IsCompleted: true}).Completed() {
		t.Error("IsCompleted should be true")
	}
}

func TestLegacyHostIsMigrated(t *testing.T) {
	cases := map[string]string{
		"https://evado.nerdlabz.com/api/v1":  "https://evado.app/api/v1",
		"https://evado.nerdlabz.com/api/v1/": "https://evado.app/api/v1",
		"http://EVADO.nerdlabz.com/api/v1":   "https://evado.app/api/v1",
		"https://evado.app/api/v1":           "https://evado.app/api/v1",
		"https://staging.example.com":        "https://staging.example.com",
		"http://127.0.0.1:8787/api/v1":       "http://127.0.0.1:8787/api/v1",
	}
	for in, want := range cases {
		if got := New(testToken, WithBaseURL(in)).BaseURL(); got != want {
			t.Errorf("WithBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A moved server must not look like a rejected token, which is what
// following the redirect without the Authorization header produced.
func TestRedirectIsReportedNotFollowed(t *testing.T) {
	var hits int
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, `{"error":"unauthorized","code":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(dest.Close)
	c, _ := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+r.URL.Path, http.StatusPermanentRedirect)
	}))

	_, _, err := c.Me(context.Background())
	if CodeOf(err) != CodeRedirected {
		t.Fatalf("error = %v, want code %q", err, CodeRedirected)
	}
	if !strings.Contains(err.Error(), dest.URL) {
		t.Errorf("error should name the new address %s: %v", dest.URL, err)
	}
	if hits != 0 {
		t.Errorf("the redirect was followed %d time(s)", hits)
	}
}

func TestSyncResponseTellsAnEmptyInvitationSnapshotFromNone(t *testing.T) {
	var empty, absent SyncResponse
	if err := json.Unmarshal([]byte(`{"todos":[],"invitations":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"todos":[]}`), &absent); err != nil {
		t.Fatal(err)
	}
	if empty.Invitations == nil || len(empty.Invitations) != 0 {
		t.Fatalf("empty snapshot decoded as %#v, want a non-nil empty slice", empty.Invitations)
	}
	if absent.Invitations != nil {
		t.Fatalf("missing snapshot decoded as %#v, want nil", absent.Invitations)
	}
}

// oversized answers every request with a body just past MaxResponseBytes.
// A hostile server need never stop; a finite one keeps a regression from
// filling memory.
func oversized(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		chunk := []byte(strings.Repeat(" ", 1<<16))
		for sent := 0; sent <= MaxResponseBytes; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}
}

func TestResponseLongerThanTheLimitIsRefused(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		c, _ := newServer(t, oversized(status))
		_, err := c.ExportTodos(context.Background())
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("status %d: error = %v, want ErrResponseTooLarge", status, err)
		}
	}
}

func TestReadResponseKeepsABodyAtTheLimit(t *testing.T) {
	raw, err := ReadResponse(strings.NewReader(strings.Repeat("x", MaxResponseBytes)))
	if err != nil || len(raw) != MaxResponseBytes {
		t.Fatalf("read %d bytes, %v; want all %d", len(raw), err, MaxResponseBytes)
	}
}
