package local

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// pending signs in, then makes n todos here that the service has not heard
// of, in order, and returns their ids.
func pendingTodos(t *testing.T, svc *service, url string, n int) (*Backend, []string) {
	t.Helper()
	b := signedIn(t, svc, url)
	var ids []string
	for i := range n {
		ids = append(ids, add(t, b.Client(), fmt.Sprintf("Todo %03d", i)).ID)
	}
	svc.requests, svc.accepted, svc.got = nil, nil, nil
	return b, ids
}

// A push the service cuts short does not park the tail as refused: the
// tail goes in the next request of the same sync, in order.
func TestDeferredTailIsSentNextRound(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b, ids := pendingTodos(t, svc, url, 200)
	svc.capacity = func(_, n int) int { return min(n, 133) }

	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 {
		t.Errorf("refused = %v, want none", res.Refused)
	}
	if res.Pushed != 200 {
		t.Errorf("pushed = %d, want 200", res.Pushed)
	}
	if len(svc.todos) != 200 {
		t.Errorf("the service holds %d todos, want 200", len(svc.todos))
	}
	if st := b.Status(); st.Pending != 0 || st.Refused != 0 {
		t.Errorf("status = %+v, want nothing pending or refused", st)
	}
	if len(svc.requests) < 2 || len(svc.requests[0]) != 200 || len(svc.requests[1]) != 67 {
		t.Fatalf("pushed %v changes per request, want 200 then 67", sizes(svc.requests))
	}
	if !slices.Equal(svc.requests[1], ids[133:]) {
		t.Errorf("the second request is not the deferred tail, in order: %v", svc.requests[1])
	}
	for _, got := range svc.requests[2:] {
		if len(got) != 0 {
			t.Errorf("sent %d changes after everything was applied", len(got))
		}
	}
}

// A service that takes one change at a time still gets there: a deferral is
// not a push, so it does not use up how often one todo may be sent.
func TestDeferralDoesNotCountAsAPush(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b, _ := pendingTodos(t, svc, url, 2*maxPushes+3)
	svc.capacity = func(_, n int) int { return min(n, 1) }

	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(svc.todos) != 2*maxPushes+3 {
		t.Errorf("the service holds %d todos, want %d", len(svc.todos), 2*maxPushes+3)
	}
	if st := b.Status(); st.Pending != 0 || st.Refused != 0 {
		t.Errorf("status = %+v, want nothing pending or refused", st)
	}
}

// A deferral that arrives in errors, as it would if the flag were ignored,
// is the same: not a refusal.
func TestDeferredErrorsAreNotRefusals(t *testing.T) {
	for name, how := range map[string]string{
		"by code":    "code",
		"by message": "message",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			svc, url := newService(t)
			b, ids := pendingTodos(t, svc, url, 10)
			svc.capacity = func(_, n int) int { return min(n, 4) }
			svc.deferAs = how

			res, err := b.Sync(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Refused) != 0 {
				t.Errorf("refused = %v, want none", res.Refused)
			}
			if len(svc.todos) != 10 {
				t.Errorf("the service holds %d todos, want 10", len(svc.todos))
			}
			if st := b.Status(); st.Pending != 0 || st.Refused != 0 {
				t.Errorf("status = %+v, want nothing pending or refused", st)
			}
			if !slices.Equal(svc.requests[1], ids[4:]) {
				t.Errorf("the second request = %v, want the tail %v", svc.requests[1], ids[4:])
			}
		})
	}
}

// What the service says about ids it was not sent, or says twice, does not
// change how many changes count as pushed.
func TestPushedCountsOnlyTheChangesSent(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b, ids := pendingTodos(t, svc, url, 3)
	svc.refuse = ids[0]
	svc.stray = []api.SyncError{
		{ID: "never-sent", Error: "unknown todo"},
		{ID: ids[0], Error: "list is read-only"},
	}

	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed != 2 || len(res.Refused) != 1 {
		t.Errorf("pushed %d, refused %v, want 2 pushed and one refused", res.Pushed, res.Refused)
	}
}

// Another message that merely mentions the words is still a refusal.
func TestAnErrorWithoutTheDeferredMarkIsStillARefusal(t *testing.T) {
	for _, e := range []api.SyncError{
		{ID: "x", Error: "list is read-only"},
		{ID: "x", Error: "the change was not applied: unknown list"},
		{ID: "x", Error: "not found", Code: "not_found"},
	} {
		if e.Deferred() {
			t.Errorf("%+v taken for a deferral", e)
		}
	}
	for _, e := range []api.SyncError{
		{ID: "x", Code: "deferred"},
		{ID: "x", Error: "not applied: too many changes in one request; send it again"},
	} {
		if !e.Deferred() {
			t.Errorf("%+v not taken for a deferral", e)
		}
	}
}

// A service that defers everything it is sent applies nothing, so sending
// the same changes again gets the same answer. The sync stops after one
// round, leaves them pending, and a later sync finishes them.
func TestSyncStopsWhenEverythingIsDeferred(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b, _ := pendingTodos(t, svc, url, 5)
	svc.capacity = func(_, _ int) int { return 0 }
	// More pages to pull after the push: the sync reads them without
	// sending the changes again.
	svc.moreSyncs = svc.syncs + 3

	res, err := b.Sync(ctx)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err = %v, want ErrIncomplete", err)
	}
	if len(res.Refused) != 0 || res.Pushed != 0 {
		t.Errorf("result = %+v, want nothing refused or pushed", res)
	}
	if st := b.Status(); st.Pending != 5 || st.Refused != 0 {
		t.Errorf("status = %+v, want five pending and none refused", st)
	}
	if len(svc.todos) != 0 {
		t.Errorf("the service holds %d todos, want none", len(svc.todos))
	}
	if len(svc.requests) != 4 {
		t.Fatalf("%d requests, want the push and the three pages after it", len(svc.requests))
	}
	if len(svc.requests[0]) != 5 {
		t.Errorf("the first request held %d changes, want 5", len(svc.requests[0]))
	}
	for i, got := range svc.requests[1:] {
		if len(got) != 0 {
			t.Errorf("request %d sent %d changes again after a round that applied none", i+2, len(got))
		}
	}

	// Later, with room, the same todos go up.
	svc.capacity = nil
	svc.moreSyncs = 0
	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); st.Pending != 0 || st.Refused != 0 || len(svc.todos) != 5 {
		t.Errorf("after the next sync: status %+v, %d on the service, want 5 sent", st, len(svc.todos))
	}
}

// Every request says the client keeps what is deferred, pushes and pulls
// alike.
func TestSyncAsksForDeferred(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	b, _ := pendingTodos(t, svc, url, 3)
	svc.moreSyncs = svc.syncs + 2

	if _, err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(svc.accepted) < 3 {
		t.Fatalf("%d requests, want at least 3", len(svc.accepted))
	}
	for i, got := range svc.accepted {
		if !got {
			t.Errorf("request %d did not set acceptDeferred", i+1)
		}
	}
}

func sizes(reqs [][]string) []int {
	var out []int
	for _, r := range reqs {
		out = append(out, len(r))
	}
	return out
}
