// Package auth keeps the session that signing in with an emailed code
// starts: a short-lived access token and the single-use refresh token that
// renews it, stored for one server and account and rotated safely when
// several requests, or several copies of evado, need a new one at once.
//
// Pasted API tokens never come here. They live in the config file and do
// not expire, so they have nothing to renew.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nerdlabz/evado-tui/internal/atomicfile"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/filelock"
)

// Credentials is a signed-in session as it is kept on disk.
type Credentials struct {
	// Server and UserID say whose session this is. It is only ever used
	// against that server, for that account.
	Server string `json:"server"`
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`

	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	Expires      time.Time `json:"expires"`
	// Scope is what the server granted at sign-in. Sessions saved before it
	// was kept have none, which reads as unknown rather than as no scope.
	Scope string `json:"scope,omitempty"`
}

// String keeps the tokens out of anything that prints credentials.
func (c Credentials) String() string {
	return fmt.Sprintf("session for %s on %s", c.UserID, c.Server)
}

// GoString does the same for %#v.
func (c Credentials) GoString() string { return c.String() }

// valid reports whether c is a whole session for this server.
func (c Credentials) valid(server string) bool {
	return c.Server == server && c.UserID != "" && c.RefreshToken != ""
}

// Store is the file a session is kept in, with a lock file beside it that
// every change to it is made under.
type Store struct {
	path string
}

// NewStore keeps the session at path.
func NewStore(path string) *Store { return &Store{path: path} }

// DefaultStore keeps the session next to the config file, in a file of its
// own so that saving preferences can never write or drop it.
func DefaultStore() (*Store, error) {
	p, err := config.Path()
	if err != nil {
		return nil, err
	}
	return NewStore(filepath.Join(filepath.Dir(p), "session.json")), nil
}

// Path is where the session is kept.
func (s *Store) Path() string { return s.path }

// load reads the session. A missing file is no session, not an error.
func (s *Store) load() (Credentials, bool, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, false, nil
	}
	if err != nil {
		return Credentials{}, false, fmt.Errorf("read %s: %w", s.path, err)
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, false, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return c, true, nil
}

// save replaces the session in one step, so a crash leaves either the old
// pair or the new one and never half of each. The caller holds the lock.
func (s *Store) save(c Credentials) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(s.path, append(b, '\n')); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

// remove deletes the session. The caller holds the lock.
func (s *Store) remove() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", s.path, err)
	}
	return nil
}

// lockWait bounds how long one copy of evado waits for another to finish
// refreshing. A refresh gives up after httpTimeout, so this is ample.
const lockWait = 45 * time.Second

// locked runs fn holding the lock file, which is what makes one process's
// read-refresh-write of the session atomic for every other process. The
// lock file is left in place: removing it would let two processes each
// hold a lock on a different file. Where the platform has no file lock,
// refreshes are serialized within one process only, and two copies of
// evado could spend the same refresh token, which the server answers by
// ending the session: a sign-in, never a leak.
func (s *Store) locked(ctx context.Context, fn func() error) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open session lock: %w", err)
	}
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	for {
		ok, err := filelock.TryLock(f)
		if err != nil {
			return fmt.Errorf("lock session: %w", err)
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("lock session: another evado is still renewing it: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer func() { _ = filelock.Unlock(f) }()
	return fn()
}
