package local

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// renewing signs requests the way a session does: with a token of its own,
// and once more with a new one when the first is turned down.
type renewing struct{ base http.RoundTripper }

func (t *renewing) RoundTrip(r *http.Request) (*http.Response, error) {
	send := func(token string, body io.ReadCloser) (*http.Response, error) {
		c := r.Clone(r.Context())
		c.Body = body
		c.Header.Set("Authorization", "Bearer "+token)
		return t.base.RoundTrip(c)
	}
	res, err := send("old", r.Body)
	if err != nil || res.StatusCode != http.StatusUnauthorized || r.GetBody == nil {
		return res, err
	}
	_ = res.Body.Close()
	body, err := r.GetBody()
	if err != nil {
		return nil, err
	}
	return send("new", body)
}

func TestAPassedOnRequestCanBeSentAgainByASession(t *testing.T) {
	var mu sync.Mutex
	var joins []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/me":
			write(map[string]any{"user": api.User{ID: "u1", TZ: "UTC"}, "lists": []api.List{}})
		case "/sync":
			write(map[string]any{"todos": []api.Todo{}, "lists": []api.List{}, "hasMore": false})
		case "/join":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			joins = append(joins, r.Header.Get("Authorization")+" "+string(b))
			mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer new" {
				w.WriteHeader(http.StatusUnauthorized)
				write(map[string]any{"error": "expired", "code": "unauthorized"})
				return
			}
			write(map[string]any{"list": api.List{ID: "l9", Name: "Shared"}})
		}
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	b, err := OpenRemote(filepath.Join(t.TempDir(), "todos.json"), Remote{})
	if err != nil {
		t.Fatal(err)
	}
	session := Remote{BaseURL: srv.URL, HTTP: &http.Client{Transport: &renewing{base: http.DefaultTransport}}}
	if _, err := b.SignInRemote(ctx, session, false); err != nil {
		t.Fatal(err)
	}
	if !b.Status().SignedIn {
		t.Fatal("not signed in through the session")
	}
	list, err := b.Client().JoinList(ctx, "abc")
	if err != nil || list.ID != "l9" {
		t.Fatalf("JoinList = %+v, %v", list, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{`Bearer old {"code":"abc"}`, `Bearer new {"code":"abc"}`}
	if len(joins) != 2 || joins[0] != want[0] || joins[1] != want[1] {
		t.Errorf("the service saw %q, want the request sent again whole with the new token", joins)
	}
}

// An answer longer than the client would read is refused here too, rather
// than read whole before it is passed on.
func TestAPassedOnAnswerLongerThanTheLimitIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/me":
			write(map[string]any{"user": api.User{ID: "u1", TZ: "UTC"}, "lists": []api.List{}})
		case "/sync":
			write(map[string]any{"todos": []api.Todo{}, "lists": []api.List{}, "hasMore": false})
		case "/me/export":
			chunk := []byte(strings.Repeat(" ", 1<<16))
			for sent := 0; sent <= api.MaxResponseBytes; sent += len(chunk) {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	b := open(t, "", "")
	if _, err := b.SignIn(ctx, "evt_x", srv.URL, false); err != nil {
		t.Fatal(err)
	}
	_, err := b.Client().Export(ctx)
	if api.CodeOf(err) != api.CodeUpstreamFailed || !strings.Contains(err.Error(), api.ErrResponseTooLarge.Error()) {
		t.Fatalf("Export error = %v, want %s saying the answer was too long", err, api.CodeUpstreamFailed)
	}
}
