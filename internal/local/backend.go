package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// address is the base URL the in-process client is given. Nothing listens
// there: the client's requests are answered by the Backend directly.
const address = api.LocalBaseURL

// Backend holds the todos on this computer. It answers API requests from
// the file, and when there is an account it keeps the file and the
// service in step.
type Backend struct {
	// mu is held while the state is read or changed, and holds the file
	// too; see disk.go.
	mu   storeLock
	path string
	st   State

	// held is the lock file while mu is held. seen is the file as this
	// backend last read or wrote it, loaded whether st came from it yet,
	// and reloads how often another copy of evado's save was read in.
	// diskErr is why the file could not be locked or read on taking mu.
	held    *os.File
	seen    fileMark
	loaded  bool
	reloads uint64
	diskErr error

	// remote is the service, or nil while there is no account, and link is
	// how it was reached.
	remote *api.Client
	link   Remote
	web    *http.Client

	// syncing stops two syncs from running at once.
	syncing bool
	lastErr error
	now     func() time.Time

	// awaiting is each edit the running sync pushed, as it was sent, until a
	// pull shows whether the service applied it; see settle. pushes counts
	// how often it pushed each todo.
	awaiting map[string]Todo
	pushes   map[string]int

	// writes counts how often the file was written, so tests can tell a
	// batch that saves once from one that saves for every todo.
	writes int
}

// Remote is how the backend reaches the service: its address, and either
// an API token or an HTTP client that authorizes each request itself, as
// a session started by an emailed code does while it renews its tokens.
type Remote struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// client is the API client for r, or nil when r has no credentials.
func (r Remote) client() *api.Client {
	if r.HTTP != nil {
		return api.New("", api.WithBaseURL(r.BaseURL), api.WithHTTPClient(r.HTTP))
	}
	if r.Token != "" {
		return api.New(r.Token, api.WithBaseURL(r.BaseURL))
	}
	return nil
}

// Open loads the todos kept at path. With a token the backend will sync
// with the service at baseURL; without one it works alone.
func Open(path, token, baseURL string) (*Backend, error) {
	return OpenRemote(path, Remote{BaseURL: baseURL, Token: token})
}

// OpenRemote is Open for any way of reaching the service.
func OpenRemote(path string, r Remote) (*Backend, error) {
	b := &Backend{path: path, now: time.Now, web: &http.Client{Timeout: 30 * time.Second}}
	b.mu.b = b
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.diskErr != nil {
		return nil, b.diskErr
	}
	b.connect(r)
	return b, nil
}

// connect points the backend at the service, or at nothing for a Remote
// with no credentials.
func (b *Backend) connect(r Remote) {
	r.Token = strings.TrimPrefix(strings.TrimSpace(r.Token), "Bearer ")
	b.link = r
	b.remote = r.client()
}

// Disconnect drops the link to the service and keeps the todos, for a
// token the service no longer accepts.
func (b *Backend) Disconnect() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connect(Remote{})
	b.lastErr = nil
}

// Client is an API client whose requests this backend answers.
func (b *Backend) Client() *api.Client {
	return api.New("local", api.WithBaseURL(address), api.WithHTTPClient(&http.Client{Transport: b}))
}

// RoundTrip answers a request in process, so the client needs no socket.
func (b *Backend) RoundTrip(r *http.Request) (*http.Response, error) {
	w := &recorder{header: http.Header{}, status: http.StatusOK}
	b.serve(w, r)
	return &http.Response{
		StatusCode: w.status,
		Status:     fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		Header:     w.header,
		Body:       io.NopCloser(&w.body),
		Request:    r,
	}, nil
}

// recorder collects a handler's answer.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *recorder) Header() http.Header         { return w.header }
func (w *recorder) WriteHeader(status int)      { w.status = status }
func (w *recorder) Write(p []byte) (int, error) { return w.body.Write(p) }

// Status is where the backend stands, for the interface to show.
type Status struct {
	// SignedIn reports whether there is an account to sync with.
	SignedIn bool
	// NeedsSignIn reports that there is a token but the todos here are not
	// yet tied to its account, so SignIn has to run before anything syncs.
	NeedsSignIn bool
	// Detached reports that the todos here belong to an account there is
	// no working token for.
	Detached bool
	// Pending is how many changes to todos and lists the service has not
	// heard about, and Refused how many todos it turned down.
	Pending, Refused int
	Syncing          bool
	LastSync         *time.Time
	// Err is why the last sync failed, or nil.
	Err error
	// Server is the service's address, or empty.
	Server string
	// Todos is how many live todos are kept here.
	Todos int
	// Invitations is how many pending invitations the last sync reported.
	Invitations int
}

// Status reports where the backend stands.
func (b *Backend) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := Status{
		SignedIn:    b.remote != nil && b.st.UserID != "",
		NeedsSignIn: b.remote != nil && b.st.UserID == "",
		Detached:    b.remote == nil && b.st.UserID != "",
		Syncing:     b.syncing,
		LastSync:    b.st.LastSync,
		Err:         b.lastErr,
		Invitations: len(b.st.Invitations),
	}
	if b.remote != nil {
		s.Server = b.remote.BaseURL()
	}
	for i := range b.st.Todos {
		t := &b.st.Todos[i]
		switch {
		case t.Refused != "":
			s.Refused++
		case t.pending():
			s.Pending++
		}
		if t.DeletedAt == nil {
			s.Todos++
		}
	}
	// With no account, a list made here waits for one rather than for a
	// sync, so it is not counted as something to send.
	if b.st.UserID != "" {
		s.Pending += len(b.st.Deleted)
		for _, l := range b.st.Lists {
			if l.Local || l.Renamed {
				s.Pending++
			}
		}
	}
	return s
}

// Offline reports whether an error means the service could not be reached,
// as opposed to it answering with a refusal.
func Offline(err error) bool {
	if err == nil || errors.Is(err, ErrIncomplete) {
		return false
	}
	var apiErr *api.Error
	return !errors.As(err, &apiErr)
}

// persist writes the state. The caller holds the lock. It writes nothing
// when the file could not be locked or read again, since the state here
// may then be older than what another copy of evado saved.
func (b *Backend) persist() error {
	if b.diskErr != nil {
		return b.diskErr
	}
	b.writes++
	return b.written(func() error { return save(b.path, b.st) })
}

// loc is the account's timezone.
func (b *Backend) loc() *time.Location {
	if l, err := time.LoadLocation(b.st.User.TZ); err == nil {
		return l
	}
	return time.Local
}

// Forget removes everything kept on this computer and the link to the
// account, which is what signing out means. Like persist it leaves the file
// alone when it could not be locked or read: removing it then could undo
// another copy of evado's save, or race one still being made, and a file
// it cannot read may be a newer version's. The state is kept too, so it
// still matches the file and no later save removes the todos by accident.
func (b *Backend) Forget() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connect(Remote{})
	b.lastErr = nil
	if b.diskErr != nil {
		return b.diskErr
	}
	b.st = fresh(b.now())
	return b.written(func() error {
		if err := os.Remove(b.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// Merge is what signing in found on the two sides.
type Merge struct {
	// Local and Server count the todos here and in the account.
	Local, Server int
	// Needed reports that both sides have todos, so the person should be
	// asked before the ones here are added to the account.
	Needed bool
}

// SignIn ties the todos on this computer to the account the token belongs
// to. When both sides already hold todos it changes nothing and reports
// that a merge is needed; calling it again with merge set goes ahead.
//
// The todos here are then sent up on the next sync. The personal list here
// becomes the account's personal list, and any other list is matched to
// one of the same name in the account, or created there.
func (b *Backend) SignIn(ctx context.Context, token, baseURL string, merge bool) (Merge, error) {
	return b.SignInRemote(ctx, Remote{BaseURL: baseURL, Token: token}, merge)
}

// SignInRemote is SignIn for any way of reaching the service.
func (b *Backend) SignInRemote(ctx context.Context, r Remote, merge bool) (Merge, error) {
	remote := r.client()
	if remote == nil {
		return Merge{}, errors.New("sign in needs a token or a session")
	}
	user, lists, err := remote.Me(ctx)
	if err != nil {
		return Merge{}, err
	}

	// Only todos the service has never seen go up. Signing in again as the
	// same account finds the rest already there, and counting them would
	// say they are being sent when nothing is.
	b.mu.Lock()
	local := 0
	for i := range b.st.Todos {
		if b.st.Todos[i].DeletedAt == nil && b.st.Todos[i].New {
			local++
		}
	}
	owner := b.st.UserID
	b.mu.Unlock()

	out := Merge{Local: local}
	switch {
	case owner == user.ID:
		// Already this account's: a new token for the same person.
	case owner != "":
		// Another account's todos are here. They are set aside rather than
		// sent to the wrong account or thrown away. If they cannot be set
		// aside, nothing changes: this computer stays with the old account.
		b.mu.Lock()
		if _, err := setAside(b.path, owner, b.st); err != nil {
			b.mu.Unlock()
			return Merge{}, fmt.Errorf("set aside the todos of the previous account: %w", err)
		}
		// Saved at once, so another copy of evado cannot bring the old
		// account's todos back before this one is adopted.
		b.st = fresh(b.now())
		err := b.persist()
		b.mu.Unlock()
		if err != nil {
			return Merge{}, err
		}
		out.Local = 0
	case local > 0 && !merge:
		probe, err := remote.Sync(ctx, api.SyncRequest{})
		if err != nil {
			return Merge{}, err
		}
		out.Server = len(probe.Todos)
		if out.Server > 0 {
			out.Needed = true
			return out, nil
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.connect(r)
	if b.st.UserID != user.ID {
		b.adopt(user, lists)
	}
	// Invitations are addressed to an account on one server. The same
	// account reached at another address is not known to be the same.
	if b.st.BaseURL != b.remote.BaseURL() {
		b.st.Invitations = nil
	}
	b.st.User = user
	b.st.BaseURL = b.remote.BaseURL()
	return out, b.persist()
}

// adopt hands the todos here to an account. The caller holds the lock.
func (b *Backend) adopt(user api.User, remote []api.List) {
	personal := ""
	byName := map[string]string{}
	for _, l := range remote {
		if l.IsPersonal {
			personal = l.ID
		}
		if l.Role == "owner" && !l.ReadOnly {
			byName[strings.ToLower(l.Name)] = l.ID
		}
	}
	// Work out where each list here goes.
	move := map[string]string{}
	var keep []List
	for _, l := range b.st.Lists {
		switch {
		case l.IsPersonal && personal != "":
			move[l.ID] = personal
		case byName[strings.ToLower(l.Name)] != "":
			move[l.ID] = byName[strings.ToLower(l.Name)]
		default:
			l.IsPersonal = false
			l.OwnerID = user.ID
			keep = append(keep, l) // created in the account on the next sync
		}
	}
	lists := make([]List, 0, len(remote)+len(keep))
	for _, l := range remote {
		lists = append(lists, List{List: l})
	}
	b.st.Lists = append(lists, keep...)

	// Every todo here is new to the account.
	kept := b.st.Todos[:0]
	for _, t := range b.st.Todos {
		if t.DeletedAt != nil {
			continue
		}
		if to, ok := move[t.ListID]; ok {
			t.ListID = to
		}
		t.New, t.Dirty, t.Base, t.Refused = true, nil, nil, ""
		kept = append(kept, t)
	}
	b.st.Todos = kept
	b.st.UserID = user.ID
	b.st.Cursor = ""
	b.st.Invitations = nil
}
