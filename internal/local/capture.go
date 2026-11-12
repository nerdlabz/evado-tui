package local

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// serviceReadTimeout bounds how long capture waits for the service to say
// how it reads the dates in a line. Past it the reader here answers, so a
// slow connection costs at most this much and never a todo.
var serviceReadTimeout = 1500 * time.Millisecond

// maxCaptureLines is how many lines one create request may hold. The
// service refuses more with too_many_lines rather than dropping the rest.
const maxCaptureLines = 20

// captureLines is the todos a create request holds, one per line. An id
// names exactly one todo, so it turns the splitting off. It is every line:
// the caller refuses a request with more than maxCaptureLines.
func captureLines(n api.NewTodo) []string {
	if n.ID != "" {
		return []string{n.Title}
	}
	var lines []string
	for _, line := range strings.Split(n.Title, "\n") {
		if line = strings.TrimSpace(line); StoredTitle(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// readings is how the service reads the dates in each line, so that a todo
// captured while signed in gets the date it would have got on the service,
// whose parser reads far more than the one here. A line the service did not
// answer for in time, or refused, is left out and read here instead. With
// no account, or when the last sync could not reach the service, nothing is
// asked and capture stays instant.
//
// It must be called without the lock held: it waits on the network.
func (b *Backend) readings(ctx context.Context, lines []string, smart *bool) map[string]Parsed {
	b.mu.Lock()
	remote, ask := b.remote, b.signedIn() && !Offline(b.lastErr)
	b.mu.Unlock()
	if !ask || len(lines) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, serviceReadTimeout)
	defer cancel()
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		out   = map[string]Parsed{}
		asked = map[string]bool{}
	)
	for _, line := range lines {
		if asked[line] {
			continue
		}
		asked[line] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p, err := remote.ParseWithSmart(ctx, line, smart); err == nil {
				mu.Lock()
				out[line] = fromService(p, line)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

// fromService is a reading from the service's /parse in the form the
// reader here gives.
func fromService(p api.ParsePreview, line string) Parsed {
	out := Parsed{Title: strings.TrimSpace(p.Title), DoAt: p.DoAt, HasTime: p.HasTime && p.DoAt != nil, Assumed: p.AssumedTime, KeptAsName: p.KeptAsName}
	if out.Title == "" {
		out.Title = line
	}
	if p.RecurrenceRule != nil && ValidRule(*p.RecurrenceRule) {
		out.Recurrence = *p.RecurrenceRule
	}
	return out
}

// reading is how a line is read: the service's reading when there is one,
// otherwise this reader's. The caller holds the lock.
func (b *Backend) reading(line string, fromService map[string]Parsed) Parsed {
	if p, ok := fromService[line]; ok {
		return p
	}
	return Parse(line, b.loc(), b.now())
}
