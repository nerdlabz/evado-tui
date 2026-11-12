package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// issuer is a stand-in for Evado's token endpoints: single-use refresh
// tokens in families, where spending one twice revokes the family.
type issuer struct {
	mu      sync.Mutex
	n       int
	family  map[string]int  // refresh or access token -> family
	spent   map[string]bool // refresh tokens already used
	revoked map[int]bool
	// refreshes counts calls to /auth/refresh, and reused how many of them
	// presented a spent token.
	refreshes, reused atomic.Int32
	// hold delays each refresh, so concurrent callers pile up behind it.
	hold time.Duration
	// drop makes the next refresh spend the token and then hang up
	// without answering.
	drop atomic.Bool
	// limit answers refreshes with 429.
	limit atomic.Bool
	// failEarly answers the next refresh with a 500 before spending the
	// token, and failLate spends it and then answers 500, as Evado does when
	// storing the new pair fails.
	failEarly, failLate atomic.Bool
	// mangle, when set, changes every pair a refresh answers with.
	mangle func(*api.TokenPair)
	// failLogout answers revocation with a server error.
	failLogout atomic.Bool
	logouts    atomic.Int32
	// bodies records each /echo body, to see a replayed request arrive whole.
	bodies []string
}

func newIssuer(t *testing.T) (*issuer, *httptest.Server) {
	t.Helper()
	is := &issuer{family: map[string]int{}, spent: map[string]bool{}, revoked: map[int]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(is.serve))
	t.Cleanup(srv.Close)
	return is, srv
}

// issue makes a new pair in family f. The caller holds mu.
func (is *issuer) issue(f int) api.TokenPair {
	is.n++
	a, r := fmt.Sprintf("acc%d", is.n), fmt.Sprintf("ref%d", is.n)
	is.family[a], is.family[r] = f, f
	return api.TokenPair{AccessToken: a, RefreshToken: r, ExpiresIn: 900, User: api.User{ID: "u1", Email: "me@example.com"}}
}

// verified is a fresh sign-in, as /auth/verify would give it.
func (is *issuer) verified() api.Verified {
	is.mu.Lock()
	defer is.mu.Unlock()
	return api.Verified{TokenPair: is.issue(is.n + 1000)}
}

func (is *issuer) serve(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "code": code})
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	switch r.URL.Path {
	case "/auth/refresh":
		is.refreshes.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&body)
		if is.limit.Load() {
			w.Header().Set("Retry-After", "30")
			fail(http.StatusTooManyRequests, "rate_limited")
			return
		}
		if is.failEarly.CompareAndSwap(true, false) {
			fail(http.StatusInternalServerError, "internal")
			return
		}
		time.Sleep(is.hold)
		is.mu.Lock()
		f, ok := is.family[body.RefreshToken]
		switch {
		case !ok || is.revoked[f]:
			is.mu.Unlock()
			fail(http.StatusBadRequest, "invalid_grant")
			return
		case is.spent[body.RefreshToken]:
			is.revoked[f] = true
			is.mu.Unlock()
			is.reused.Add(1)
			fail(http.StatusUnauthorized, "refresh_token_reused")
			return
		}
		is.spent[body.RefreshToken] = true
		if is.failLate.CompareAndSwap(true, false) {
			is.mu.Unlock()
			fail(http.StatusInternalServerError, "internal")
			return
		}
		pair := is.issue(f)
		if is.mangle != nil {
			is.mangle(&pair)
		}
		is.mu.Unlock()
		if is.drop.CompareAndSwap(true, false) {
			// The token is spent, and the answer never arrives.
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(pair)
	case "/auth/token/logout":
		is.logouts.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&body)
		if is.failLogout.Load() {
			fail(http.StatusInternalServerError, "internal")
			return
		}
		is.mu.Lock()
		if f, ok := is.family[body.RefreshToken]; ok {
			is.revoked[f] = true
		}
		is.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	default:
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		is.mu.Lock()
		f, ok := is.family[token]
		good := ok && !is.revoked[f] && strings.HasPrefix(token, "acc")
		if good && r.URL.Path == "/echo" {
			b, _ := io.ReadAll(r.Body)
			is.bodies = append(is.bodies, string(b))
		}
		is.mu.Unlock()
		if !good {
			fail(http.StatusUnauthorized, "unauthorized")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"id": "u1"}})
	}
}

// clock is a settable now for sessions, so a test can let tokens expire.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// started signs in and saves the session, as the TUI does once a sign-in
// has gone through.
func started(t *testing.T, is *issuer, url string, clk *clock) (*Session, *Store) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "session.json"))
	s := Start(store, url, is.verified())
	s.now = clk.now
	s.cur.Expires = clk.now().Add(15 * time.Minute)
	if err := s.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, store
}

// reopen is another run of evado, or another copy running beside this
// one, reading the same file through a store of its own.
func reopen(t *testing.T, store *Store, url string, clk *clock) *Session {
	t.Helper()
	s, err := Open(NewStore(store.Path()), url)
	if err != nil || s == nil {
		t.Fatalf("Open = %v, %v; want the saved session", s, err)
	}
	s.now = clk.now
	return s
}

func onDisk(t *testing.T, store *Store) (Credentials, bool) {
	t.Helper()
	c, ok, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	return c, ok
}

func get(t *testing.T, s *Session, url string) int {
	t.Helper()
	res, err := s.Client().Get(url + "/me")
	if err != nil {
		t.Fatal(err)
	}
	drain(res)
	return res.StatusCode
}

func TestAnExpiringTokenIsRenewedAndTheNewPairSavedTogether(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, store := started(t, is, srv.URL, clk)
	before, _ := onDisk(t, store)

	clk.add(14*time.Minute + 30*time.Second) // inside the minute before expiry
	if got := get(t, s, srv.URL); got != http.StatusOK {
		t.Fatalf("status = %d, want 200 after renewing", got)
	}
	if n := is.refreshes.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
	after, ok := onDisk(t, store)
	if !ok || after.AccessToken == before.AccessToken || after.RefreshToken == before.RefreshToken {
		t.Fatalf("stored pair was not replaced as a whole: before %q/%q after %q/%q",
			before.AccessToken, before.RefreshToken, after.AccessToken, after.RefreshToken)
	}
	if !after.Expires.After(clk.now()) {
		t.Errorf("expires = %v, want after now", after.Expires)
	}
	// A fresh token is not renewed again.
	get(t, s, srv.URL)
	if n := is.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d, want no second one for a fresh token", n)
	}
}

func TestTheSessionFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions do not apply on Windows")
	}
	is, srv := newIssuer(t)
	_, store := started(t, is, srv.URL, &clock{t: time.Now()})
	for _, p := range []string{store.Path(), store.Path() + ".lock"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), perm)
		}
	}
}

func TestConcurrentRequestsShareOneRefresh(t *testing.T) {
	is, srv := newIssuer(t)
	is.hold = 50 * time.Millisecond
	clk := &clock{t: time.Now()}
	s, _ := started(t, is, srv.URL, clk)
	clk.add(time.Hour)

	var wg sync.WaitGroup
	var bad atomic.Int32
	for range 40 {
		wg.Go(func() {
			res, err := s.Client().Get(srv.URL + "/me")
			if err != nil || res.StatusCode != http.StatusOK {
				bad.Add(1)
			}
			if res != nil {
				drain(res)
			}
		})
	}
	wg.Wait()
	if n := is.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d, want exactly 1", n)
	}
	if bad.Load() != 0 || is.reused.Load() != 0 {
		t.Errorf("%d requests failed, %d reused a spent token", bad.Load(), is.reused.Load())
	}
}

func TestASecondProcessUsesThePairTheFirstRenewed(t *testing.T) {
	is, srv := newIssuer(t)
	is.hold = 30 * time.Millisecond
	clk := &clock{t: time.Now()}
	first, store := started(t, is, srv.URL, clk)
	second := reopen(t, store, srv.URL, clk)
	clk.add(time.Hour)

	// Both need a new pair at the same moment, each through its own
	// handle on the file.
	var wg sync.WaitGroup
	tokens := make([]string, 2)
	errs := make([]error, 2)
	for i, s := range []*Session{first, second} {
		wg.Go(func() { tokens[i], errs[i] = s.Token(context.Background()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	if n := is.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d, want the second to use the first's pair", n)
	}
	if is.reused.Load() != 0 {
		t.Error("a spent refresh token was sent again")
	}
	if tokens[0] != tokens[1] {
		t.Errorf("tokens = %q and %q, want the same renewed one", tokens[0], tokens[1])
	}
	// And the next renewal, by either, spends the newest token.
	clk.add(time.Hour)
	if _, err := second.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := is.refreshes.Load(); n != 2 || is.reused.Load() != 0 {
		t.Errorf("refreshes = %d reused = %d, want 2 and none", n, is.reused.Load())
	}
}

func TestARestartCarriesOnWithTheSavedSession(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, store := started(t, is, srv.URL, clk)
	clk.add(time.Hour)
	if _, err := s.Token(context.Background()); err != nil {
		t.Fatal(err)
	}

	again := reopen(t, store, srv.URL+"/", clk) // the same server, typed differently
	if again.UserID() != "u1" || again.Email() != "me@example.com" {
		t.Errorf("reloaded session is for %q <%s>", again.UserID(), again.Email())
	}
	if got := get(t, again, srv.URL); got != http.StatusOK {
		t.Errorf("status = %d, want the reloaded pair to work", got)
	}
	if n := is.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d, want the reloaded pair used as it is", n)
	}

	// A session is only ever used with the server it came from.
	if other, err := Open(NewStore(store.Path()), "https://elsewhere.example/api/v1"); err != nil || other != nil {
		t.Errorf("Open for another server = %v, %v; want nothing", other, err)
	}
}

func TestARejectedAccessTokenIsRenewedAndTheRequestSentAgain(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, _ := started(t, is, srv.URL, clk)
	// The server stops accepting the access token before its time, as if
	// this computer's clock were slow.
	is.mu.Lock()
	delete(is.family, s.cur.AccessToken)
	is.mu.Unlock()

	res, err := s.Client().Post(srv.URL+"/echo", "application/json", strings.NewReader(`{"title":"milk"}`))
	if err != nil {
		t.Fatal(err)
	}
	drain(res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 on the second try", res.StatusCode)
	}
	if n := is.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d, want 1", n)
	}
	if len(is.bodies) != 1 || is.bodies[0] != `{"title":"milk"}` {
		t.Errorf("bodies = %q, want the request sent again whole", is.bodies)
	}
}

func TestARevokedSessionEndsAndIsForgotten(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, store := started(t, is, srv.URL, clk)
	// Signed out from another device: the whole family is revoked.
	is.mu.Lock()
	is.revoked[is.family[s.cur.RefreshToken]] = true
	is.mu.Unlock()

	if got := get(t, s, srv.URL); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
	if _, ok := onDisk(t, store); ok {
		t.Error("a session the server revoked is still on disk")
	}
	if _, err := s.Token(context.Background()); !errors.Is(err, ErrEnded) {
		t.Errorf("Token = %v, want ErrEnded", err)
	}
	// Once ended, it asks nothing more of the server.
	n := is.refreshes.Load()
	if got := get(t, s, srv.URL); got != http.StatusUnauthorized || is.refreshes.Load() != n {
		t.Errorf("status = %d refreshes = %d, want a 401 with no new refresh", got, is.refreshes.Load())
	}
}

// Evado spends a refresh token before it stores the new pair, so a lost
// answer or a 5xx can leave the token spent. Sending it again gets a 401
// for reuse, which ends the session as giving up would have; when it was
// not spent, sending it again keeps the person signed in.
func TestARefreshWithNoSettledAnswerIsTriedAgain(t *testing.T) {
	for _, c := range []struct {
		name  string
		fail  func(*issuer)
		spent bool
	}{
		{"answer lost after the token was spent", func(is *issuer) { is.drop.Store(true) }, true},
		{"server error after the token was spent", func(is *issuer) { is.failLate.Store(true) }, true},
		{"server error before the token was spent", func(is *issuer) { is.failEarly.Store(true) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			is, srv := newIssuer(t)
			clk := &clock{t: time.Now()}
			s, store := started(t, is, srv.URL, clk)
			clk.add(time.Hour)
			c.fail(is)

			if _, err := s.Token(context.Background()); err == nil || errors.Is(err, ErrEnded) {
				t.Fatalf("Token = %v, want an error that keeps the session", err)
			}
			if _, ok := onDisk(t, store); !ok {
				t.Fatal("the session was dropped before the server said whether its token was spent")
			}

			_, err := s.Token(context.Background())
			if c.spent {
				if !errors.Is(err, ErrEnded) {
					t.Fatalf("Token = %v, want ErrEnded once the server says the token was reused", err)
				}
				if _, ok := onDisk(t, store); ok {
					t.Error("the ended session is still on disk")
				}
				if is.reused.Load() != 1 {
					t.Errorf("reused = %d, want the one retry to be how the server said so", is.reused.Load())
				}
			} else {
				if err != nil {
					t.Fatalf("Token = %v, want the unspent token renewed on the second try", err)
				}
				if got := get(t, s, srv.URL); got != http.StatusOK {
					t.Errorf("status = %d, want the renewed session to work", got)
				}
			}
			if n := is.refreshes.Load(); n != 2 {
				t.Errorf("refreshes = %d, want the token sent twice, no more", n)
			}
		})
	}
}

// A refresh answered 200 without a pair the session can use is an error,
// never an empty token to send.
func TestARefreshAnsweredWithNoUsablePairIsAnError(t *testing.T) {
	for _, c := range []struct {
		name   string
		mangle func(*api.TokenPair)
	}{
		{"no access token", func(p *api.TokenPair) { p.AccessToken = "" }},
		{"no refresh token", func(p *api.TokenPair) { p.RefreshToken = "" }},
		{"another account", func(p *api.TokenPair) { p.User.ID = "u2" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			is, srv := newIssuer(t)
			is.mangle = c.mangle
			clk := &clock{t: time.Now()}
			s, store := started(t, is, srv.URL, clk)
			before, _ := onDisk(t, store)
			clk.add(time.Hour)

			// Not a request sent with an empty token and turned down as if
			// signed out, but the renewal's own error.
			res, err := s.Client().Get(srv.URL + "/me")
			if err == nil {
				drain(res)
				t.Fatalf("status = %d, want the renewal's error", res.StatusCode)
			}
			if errors.Is(err, ErrEnded) {
				t.Fatalf("error = %v, want one that keeps the session", err)
			}
			if after, ok := onDisk(t, store); !ok || after.RefreshToken != before.RefreshToken {
				t.Fatal("an unusable answer replaced or dropped the saved pair")
			}
			// The server spent the token, so sending it again is how the
			// session learns that it ended.
			token, err := s.Token(context.Background())
			if !errors.Is(err, ErrEnded) || token != "" {
				t.Errorf("Token = %q, %v; want ErrEnded once the server says the token was reused", token, err)
			}
		})
	}
}

func TestAServerErrorDuringRenewalReadsAsAServerError(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, _ := started(t, is, srv.URL, clk)
	clk.add(time.Hour)
	is.failEarly.Store(true)
	// Not a 401, which would send the person to sign in, and not a
	// connection error, which would read as being offline.
	if got := get(t, s, srv.URL); got != http.StatusInternalServerError {
		t.Errorf("status = %d, want the server's 500", got)
	}
}

// A copy of evado that died after sending a refresh leaves the old pair on
// disk. The next one sends it and the server settles what happened.
func TestARefreshCutShortByACrashIsSettledByTheServer(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	_, store := started(t, is, srv.URL, clk)
	c, _ := onDisk(t, store)
	// The dead copy's refresh reached the server and spent the token.
	if res, err := http.Post(srv.URL+"/auth/refresh", "application/json",
		strings.NewReader(`{"refresh_token":"`+c.RefreshToken+`"}`)); err == nil {
		_ = res.Body.Close()
	}

	again := reopen(t, store, srv.URL, clk)
	// The access token is still good until it expires.
	if got := get(t, again, srv.URL); got != http.StatusOK {
		t.Errorf("status = %d, want the unexpired access token used", got)
	}
	clk.add(time.Hour)
	if _, err := again.Token(context.Background()); !errors.Is(err, ErrEnded) {
		t.Fatalf("Token = %v, want ErrEnded", err)
	}
	if _, ok := onDisk(t, store); ok {
		t.Error("the ended session is still on disk")
	}
}

func TestARefreshThatNeverLeftKeepsTheSession(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, store := started(t, is, srv.URL, clk)
	clk.add(time.Hour)
	srv.Close() // offline: nothing is listening

	_, err := s.Token(context.Background())
	if err == nil || errors.Is(err, ErrEnded) {
		t.Fatalf("Token = %v, want a connection error that keeps the session", err)
	}
	c, ok := onDisk(t, store)
	if !ok || c.RefreshToken == "" {
		t.Errorf("stored = %v, want it kept for the next try", ok)
	}
	// Seen through the client, being offline is an error, not a 401 that
	// would send the person to sign in.
	if _, err := s.Client().Get(srv.URL + "/me"); err == nil {
		t.Error("an offline request succeeded")
	}
	if _, err := s.Token(context.Background()); errors.Is(err, ErrEnded) {
		t.Error("being offline ended the session")
	}
}

func TestARateLimitedRefreshKeepsTheSessionAndSaysWhen(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, store := started(t, is, srv.URL, clk)
	clk.add(time.Hour)
	is.limit.Store(true)

	res, err := s.Client().Get(srv.URL + "/me")
	if err != nil {
		t.Fatal(err)
	}
	drain(res)
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") != "30" {
		t.Errorf("status = %d retry-after = %q, want 429 and 30", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if _, ok := onDisk(t, store); !ok {
		t.Error("a refresh turned away by the rate limit lost the session")
	}
	is.limit.Store(false)
	if got := get(t, s, srv.URL); got != http.StatusOK {
		t.Errorf("status = %d, want the session to work once the limit lifts", got)
	}
}

func TestAnUnsavedSessionRenewsInMemory(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	store := NewStore(filepath.Join(t.TempDir(), "session.json"))
	s := Start(store, srv.URL, is.verified())
	s.now = clk.now
	clk.add(time.Hour)
	if got := get(t, s, srv.URL); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if _, ok := onDisk(t, store); ok {
		t.Error("a session nobody saved was written to disk")
	}
	if err := s.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, ok := onDisk(t, store); !ok || c.AccessToken != s.cur.AccessToken {
		t.Error("Save did not keep the renewed pair")
	}
}

func TestSignOutRevokesTheNewestTokenAndForgetsIt(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	s, store := started(t, is, srv.URL, clk)
	// Another copy of evado renewed the pair since this one looked.
	other := reopen(t, store, srv.URL, clk)
	clk.add(time.Hour)
	if _, err := other.Token(context.Background()); err != nil {
		t.Fatal(err)
	}

	local, remote := s.SignOut(context.Background())
	if local != nil || remote != nil {
		t.Fatalf("SignOut = %v, %v", local, remote)
	}
	if _, ok := onDisk(t, store); ok {
		t.Error("the session is still on disk")
	}
	// Revoking the family cuts off the other copy too.
	if got := get(t, other, srv.URL); got != http.StatusUnauthorized {
		t.Errorf("status = %d, want the revoked family refused", got)
	}
	if is.reused.Load() != 0 {
		t.Error("sign-out sent a spent refresh token")
	}
}

func TestSignOutForgetsLocallyWhenRevocationFails(t *testing.T) {
	is, srv := newIssuer(t)
	s, store := started(t, is, srv.URL, &clock{t: time.Now()})
	is.failLogout.Store(true)

	local, remote := s.SignOut(context.Background())
	if local != nil {
		t.Fatalf("local = %v", local)
	}
	if remote == nil {
		t.Error("a failed revocation was not reported")
	}
	if _, ok := onDisk(t, store); ok {
		t.Error("the session is still on disk after a failed revocation")
	}
	if _, err := s.Token(context.Background()); !errors.Is(err, ErrEnded) {
		t.Errorf("Token = %v, want the session over here", err)
	}
}

func TestCredentialsNeverPrintTheirTokens(t *testing.T) {
	c := Credentials{Server: "https://x/api/v1", UserID: "u1", AccessToken: "acc_secret", RefreshToken: "ref_secret"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(verb, c); strings.Contains(out, "secret") {
			t.Errorf("%s printed a token: %s", verb, out)
		}
	}
}

func TestTheGrantedScopeIsKeptAndSurvivesARenewalThatOmitsIt(t *testing.T) {
	is, srv := newIssuer(t)
	clk := &clock{t: time.Now()}
	store := NewStore(filepath.Join(t.TempDir(), "session.json"))
	v := is.verified()
	v.Scope = "todos.read todos.write"
	s := Start(store, srv.URL, v)
	s.now = clk.now
	if err := s.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := reopen(t, store, srv.URL, clk).Scope(); got != v.Scope {
		t.Fatalf("scope after reopening = %q, want %q", got, v.Scope)
	}

	clk.add(time.Hour)
	if _, err := s.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.Scope(); got != v.Scope {
		t.Errorf("scope after a renewal = %q, want it kept", got)
	}
}

func TestASessionSavedWithoutAScopeReadsAsUnknown(t *testing.T) {
	_, srv := newIssuer(t)
	path := filepath.Join(t.TempDir(), "session.json")
	old := `{"server":"` + Server(srv.URL) + `","userId":"u1","accessToken":"a","refreshToken":"r","expires":"2030-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(NewStore(path), srv.URL)
	if err != nil || s == nil {
		t.Fatalf("Open = %v, %v", s, err)
	}
	if got := s.Scope(); got != "" {
		t.Errorf("scope = %q, want unknown", got)
	}
}
