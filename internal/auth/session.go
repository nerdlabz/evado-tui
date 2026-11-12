package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// ErrEnded means the session cannot be renewed: it was revoked, signed
// out, replaced, or a refresh was lost on the way. Only signing in again
// helps.
var ErrEnded = errors.New("the session ended; sign in again")

// errUnusableRenewal is a refresh the server said yes to without a token
// pair this session can use.
var errUnusableRenewal = errors.New("could not renew the session: the server's answer had no usable token pair")

const (
	// early is how long before its expiry an access token is renewed, so
	// a request is never sent with one that runs out on the way.
	early = time.Minute
	// defaultLifetime is assumed when a server does not say how long an
	// access token lasts. It is what Evado issues.
	defaultLifetime = 15 * time.Minute
	// callTimeout bounds a refresh or a revocation.
	callTimeout = 30 * time.Second
)

// Session is a signed-in session in use: the current token pair, renewed
// when it is about to expire or the server turns it down.
//
// Refresh tokens are single use, so renewals are serialized twice over:
// by mu between the requests of this process, and by the store's lock
// file between copies of evado sharing the session. Whoever gets the lock
// reads the file again first, and uses a pair another copy already
// renewed rather than spending the old refresh token a second time.
type Session struct {
	store  *Store
	server string
	// api makes the unauthenticated refresh and revocation calls.
	api *api.Client
	now func() time.Time

	mu  sync.Mutex
	cur Credentials
	// saved says cur is kept in the store, so renewing it goes through the
	// lock and the file. A session that is not saved yet renews in memory.
	saved bool
	ended bool
}

// Server is the address a session belongs to, in the form the client
// uses, so an address typed with a trailing slash still matches.
func Server(baseURL string) string { return api.New("", api.WithBaseURL(baseURL)).BaseURL() }

func newSession(store *Store, server string) *Session {
	return &Session{
		store:  store,
		server: server,
		api:    api.New("", api.WithBaseURL(server)),
		now:    time.Now,
	}
}

// Open resumes the session kept in store for the server at baseURL. It
// returns nil when there is none, or when the one kept belongs to another
// server.
func Open(store *Store, baseURL string) (*Session, error) {
	server := Server(baseURL)
	c, ok, err := store.load()
	if err != nil || !ok || !c.valid(server) {
		return nil, err
	}
	s := newSession(store, server)
	s.cur, s.saved = c, true
	return s, nil
}

// Start begins a session from a verified code. It lives in memory until
// Save, so a sign-in that goes no further leaves the session on disk, and
// whoever it belongs to, as they were.
func Start(store *Store, baseURL string, v api.Verified) *Session {
	s := newSession(store, Server(baseURL))
	s.cur = s.credentials(v.TokenPair, v.User.ID, v.User.Email)
	return s
}

func (s *Session) credentials(p api.TokenPair, userID, email string) Credentials {
	life := time.Duration(p.ExpiresIn) * time.Second
	if life <= 0 {
		life = defaultLifetime
	}
	return Credentials{
		Server:       s.server,
		UserID:       userID,
		Email:        email,
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		Expires:      s.now().Add(life),
		Scope:        p.Scope,
	}
}

// UserID is the account the session belongs to.
func (s *Session) UserID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.UserID
}

// Email is the address the session was started with.
func (s *Session) Email() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.Email
}

// Scope is what the server granted when the session started, or "" when
// that is not known, as for a session saved by an older version.
func (s *Session) Scope() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.Scope
}

// BaseURL is the server the session belongs to.
func (s *Session) BaseURL() string { return s.server }

// Save keeps the session in the store, replacing any session there, so
// the next run and other copies of evado carry on with it.
func (s *Session) Save(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return ErrEnded
	}
	if err := s.store.locked(ctx, func() error { return s.store.save(s.cur) }); err != nil {
		return err
	}
	s.saved = true
	return nil
}

// Token returns an access token to send, renewing the pair first when the
// current one is about to expire.
func (s *Session) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return "", ErrEnded
	}
	if !s.expiring(s.cur) {
		return s.cur.AccessToken, nil
	}
	return s.renew(ctx, "")
}

// Renew returns an access token to use instead of rejected, which the
// server turned down. When another request has renewed the pair since
// rejected was handed out, that pair is used and nothing is spent.
func (s *Session) Renew(ctx context.Context, rejected string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return "", ErrEnded
	}
	if s.cur.AccessToken != rejected && !s.expiring(s.cur) {
		return s.cur.AccessToken, nil
	}
	return s.renew(ctx, rejected)
}

func (s *Session) expiring(c Credentials) bool {
	return !s.now().Before(c.Expires.Add(-early))
}

// renew gets a working access token, spending the refresh token only when
// nobody else has. The caller holds mu.
func (s *Session) renew(ctx context.Context, rejected string) (string, error) {
	if !s.saved {
		return s.rotate(ctx, false)
	}
	var token string
	err := s.store.locked(ctx, func() error {
		disk, ok, err := s.store.load()
		if err != nil {
			// With the file unreadable there is no telling whether another
			// copy of evado already spent the token, and no way to keep a new
			// pair either. Retrying would only fail the same way.
			return s.end()
		}
		if !ok || disk.Server != s.server || disk.UserID != s.cur.UserID {
			// Signed out, or signed in as someone else, by another copy of
			// evado. What is on disk is theirs, and stays.
			return s.end()
		}
		if disk.RefreshToken != s.cur.RefreshToken {
			// Another copy of evado renewed it while this one waited.
			s.cur = disk
			if disk.AccessToken != rejected && !s.expiring(disk) {
				token = disk.AccessToken
				return nil
			}
		}
		// A copy of evado that died after sending a refresh leaves the old
		// pair here. Sending it again is safe; see rotate.
		token, err = s.rotate(ctx, true)
		return err
	})
	return token, err
}

// rotate spends the refresh token on a new pair and keeps it, in the store
// too when saved is set. The caller holds mu, and the lock when saved.
func (s *Session) rotate(ctx context.Context, saved bool) (string, error) {
	old := s.cur
	// A refresh that has been sent is seen through even if the request
	// that wanted it gives up: abandoning it would lose the new pair and
	// leave the old token spent.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	p, err := s.api.RefreshTokens(ctx, old.RefreshToken)
	if err == nil && (p.AccessToken == "" || p.RefreshToken == "" || (p.User.ID != "" && p.User.ID != old.UserID)) {
		// A yes with no pair, or with one for another account, cannot be
		// used. It is taken as an answer that never came, not as a renewal
		// that worked with an empty token.
		err = errUnusableRenewal
	}

	switch {
	case err == nil:
		next := s.credentials(p, old.UserID, old.Email)
		if p.User.Email != "" {
			next.Email = p.User.Email
		}
		if next.Scope == "" {
			// A refresh answer may leave the scope out; it does not shrink.
			next.Scope = old.Scope
		}
		if saved {
			if err := s.store.save(next); err != nil {
				// The new pair works but cannot be kept, and the file still
				// says a refresh is in doubt. Carrying on in memory would leave
				// the next run to find that and end the session anyway, so it
				// ends now, where the reason can be seen.
				_ = s.store.remove()
				_ = s.end()
				return "", err
			}
		}
		s.cur = next
		return next.AccessToken, nil
	case refused(err):
		// The server read the token and turned it down: expired, unknown,
		// or already spent, which has just revoked this sign-in.
		if saved {
			_ = s.store.remove()
		}
		return "", s.end()
	default:
		// No answer the session can end on: offline, rate limited, a
		// server error, an answer lost after the token was sent, or one
		// with no pair to use. The token is kept and sent again next time,
		// which is safe even if the server did spend it. Evado spends a
		// token before it stores the new pair, so a 5xx or a lost answer can
		// leave it spent with the new pair never seen here. Sent again, it
		// gets a 401 for reuse, which revokes only this sign-in (no other
		// device, no email) and ends the session, as giving up now would. If
		// it was not spent, the retry renews it and nobody has to sign in
		// again.
		return "", err
	}
}

// refused reports whether the server answered a refresh with a refusal
// that settles it, as opposed to failing or being too busy to say.
func refused(err error) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 &&
		apiErr.StatusCode != http.StatusTooManyRequests
}

// end marks the session over and drops its tokens from memory.
func (s *Session) end() error {
	s.ended = true
	s.cur.AccessToken, s.cur.RefreshToken = "", ""
	return ErrEnded
}

// Forget removes the session from this computer. revoke asks the server
// to end it as well; it can be called later, away from anything waiting
// on the removal, or not at all for a session the server has already
// stopped accepting. A session on disk that belongs to someone else is
// left alone.
func (s *Session) Forget(ctx context.Context) (revoke func(context.Context) error, err error) {
	refresh, err := s.drop(ctx)
	return func(ctx context.Context) error {
		if refresh == "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		return s.api.EndSession(ctx, refresh)
	}, err
}

// SignOut removes the session from this computer, then asks the server to
// revoke it. Removing it does not wait on the server: local is why it
// could not be removed, and remote why the server could not be told,
// which leaves the session alive there until it lapses.
func (s *Session) SignOut(ctx context.Context) (local, remote error) {
	revoke, local := s.Forget(ctx)
	return local, revoke(ctx)
}

// drop ends the session and removes it from the store, returning the
// newest refresh token it had, which another copy of evado may have
// rotated since this one last looked.
func (s *Session) drop(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refresh := s.cur.RefreshToken
	var err error
	if s.saved {
		err = s.store.locked(ctx, func() error {
			disk, ok, err := s.store.load()
			if err != nil {
				// A file that cannot be read cannot be checked either, and
				// signing out must not leave a session behind.
				return s.store.remove()
			}
			if !ok || disk.Server != s.server || disk.UserID != s.cur.UserID {
				return nil
			}
			refresh = disk.RefreshToken
			return s.store.remove()
		})
	}
	_ = s.end()
	return refresh, err
}
