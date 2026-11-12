package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func call(t *testing.T, h http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, prefix+path, strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestEveryRequestNeedsTheToken(t *testing.T) {
	h := newServer(newStore(), "evt_dev")
	for _, auth := range []string{"", "Bearer evt_other", "evt_dev", "Bearer evt_dev "} {
		for _, path := range []string{"/me", "/todos", "/sync", "/lists/l_home"} {
			if w := call(t, h, http.MethodGet, path, auth, ""); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"unauthorized"`) {
				t.Errorf("GET %s with %q: %d %s, want 401 unauthorized", path, auth, w.Code, w.Body)
			}
		}
	}
	if w := call(t, h, http.MethodGet, "/me", "Bearer evt_dev", ""); w.Code != http.StatusOK {
		t.Errorf("GET /me with the token: %d %s", w.Code, w.Body)
	}
}

func TestOnlyLoopbackUnlessAskedFor(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8787", "localhost:8787", "[::1]:8787", "127.0.0.2:0"} {
		if err := checkAddr(addr, false); err != nil {
			t.Errorf("%s: %v, want it allowed", addr, err)
		}
	}
	for _, addr := range []string{":8787", "0.0.0.0:8787", "[::]:8787", "192.168.1.5:8787", "example.com:8787", "8787"} {
		if checkAddr(addr, false) == nil {
			t.Errorf("%s was allowed without -remote", addr)
		}
	}
	if err := checkAddr(":8787", true); err != nil {
		t.Errorf("-remote should allow every interface: %v", err)
	}
}

// Run under the race detector (mise run test-race), this fails if any
// handler touches the store without the lock.
func TestConcurrentRequestsShareTheStoreSafely(t *testing.T) {
	h := newServer(newStore(), "evt_dev")
	reqs := []struct{ method, path, body string }{
		{http.MethodPost, "/todos", `{"title":"Call the bank tomorrow"}`},
		{http.MethodGet, "/todos", ""},
		{http.MethodPatch, "/todos/t1", `{"title":"Pay it"}`},
		{http.MethodPost, "/todos/t1/complete", ""},
		{http.MethodPost, "/todos/t1/checklist", `{"title":"Find the card"}`},
		{http.MethodPatch, "/checklist/c1", `{"title":"Read it again"}`},
		{http.MethodPost, "/lists", `{"name":"Errands"}`},
		{http.MethodPatch, "/lists/l_home", `{"name":"Home"}`},
		{http.MethodPatch, "/me", `{"tz":"UTC"}`},
		{http.MethodGet, "/me", ""},
		{http.MethodPost, "/sync", `{"changes":[{"id":"t9","title":"From another device","updatedAt":"2030-01-01T00:00:00Z"}]}`},
		{http.MethodPost, "/invitations/inv_club/decline", ""},
	}
	var wg sync.WaitGroup
	for range 4 {
		for _, rq := range reqs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				call(t, h, rq.method, rq.path, "Bearer evt_dev", rq.body)
			}()
		}
	}
	wg.Wait()
	if w := call(t, h, http.MethodGet, "/todos?status=all", "Bearer evt_dev", ""); !strings.Contains(w.Body.String(), "Call the bank") {
		t.Errorf("the todos added are not there: %s", w.Body)
	}
}
