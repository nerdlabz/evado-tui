package main

import (
	"encoding/json"
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

func TestCreatingAListAnswersTheListAsTheServiceDoes(t *testing.T) {
	h := newServer(newStore(), "evt_dev")
	made := func(w *httptest.ResponseRecorder) (map[string]any, bool) {
		t.Helper()
		var out struct {
			List       map[string]any   `json:"list"`
			Lists      []map[string]any `json:"lists"`
			Idempotent bool             `json:"idempotent"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v", w.Body, err)
		}
		if out.Lists != nil {
			t.Fatalf("answered lists, want one list: %s", w.Body)
		}
		return out.List, out.Idempotent
	}

	w := call(t, h, http.MethodPost, "/lists", "Bearer evt_dev", `{"name":"Errands"}`)
	l, _ := made(w)
	if w.Code != http.StatusCreated || l["name"] != "Errands" || l["id"] == "" || l["role"] != "owner" {
		t.Errorf("create: %d %s, want 201 and the list", w.Code, w.Body)
	}

	const id = "6f9a1d52-3c1e-4b6e-8f0a-2d9b7c4e5a10"
	first := call(t, h, http.MethodPost, "/lists", "Bearer evt_dev", `{"id":"`+id+`","name":"Trip"}`)
	l, repeat := made(first)
	if first.Code != http.StatusCreated || l["id"] != id || repeat {
		t.Errorf("create with an id: %d %s, want 201 under that id", first.Code, first.Body)
	}
	again := call(t, h, http.MethodPost, "/lists", "Bearer evt_dev", `{"id":"`+id+`","name":"Trip, again"}`)
	l, repeat = made(again)
	if again.Code != http.StatusOK || l["id"] != id || l["name"] != "Trip" || !repeat {
		t.Errorf("repeat: %d %s, want 200 and the list as it is", again.Code, again.Body)
	}

	var all struct {
		Lists []struct{ Name string } `json:"lists"`
	}
	if err := json.Unmarshal(call(t, h, http.MethodGet, "/lists", "Bearer evt_dev", "").Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	trips := 0
	for _, x := range all.Lists {
		if x.Name == "Trip" {
			trips++
		}
	}
	if trips != 1 {
		t.Errorf("%d lists named Trip, want a repeat to make no second", trips)
	}
}
