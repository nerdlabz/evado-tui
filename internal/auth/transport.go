package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// Client is an HTTP client whose requests to the session's server carry
// its access token. It is the one place renewal happens: an api.Client or
// a proxy built on it never sees a refresh token, and needs no token of
// its own.
func (s *Session) Client() *http.Client {
	var server string
	if u, err := url.Parse(s.server); err == nil {
		server = origin(u)
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		// Like api.New's client: a redirect would drop the token, and the
		// API client reports it instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &transport{s: s, base: http.DefaultTransport, origin: server},
	}
}

type transport struct {
	s    *Session
	base http.RoundTripper
	// origin is the session server's scheme, host and port, the only place
	// its token goes.
	origin string
}

// origin is u's scheme, host and port, with the port the scheme implies
// when none is given, or "" when u is not absolute.
func origin(u *url.URL) string {
	if u.Scheme == "" || u.Host == "" {
		return ""
	}
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// RoundTrip sends req with a current access token. A token the server
// turns down early, because it was revoked or this computer's clock is
// off, gets one renewal and the request is sent once more.
//
// A request for anywhere but the session's server, including one a
// redirect led to, goes as it is: the token would let whoever is there act
// as this account, and their 401 says nothing about the session.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if o := origin(req.URL); o == "" || o != t.origin {
		return t.base.RoundTrip(req)
	}
	token, err := t.s.Token(req.Context())
	if err != nil {
		closeBody(req)
		return refusal(req, err)
	}
	res, err := t.base.RoundTrip(authorized(req, req.Body, token))
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		return res, err
	}
	// A body that cannot be read again cannot be sent again, so the
	// server's answer stands.
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return res, nil
	}
	next, err := t.s.Renew(req.Context(), token)
	if errors.Is(err, ErrEnded) {
		return res, nil
	}
	drain(res)
	if err != nil {
		return refusal(req, err)
	}
	var body io.ReadCloser
	if req.GetBody != nil {
		if body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	return t.base.RoundTrip(authorized(req, body, next))
}

// authorized is a copy of req with body and the bearer token.
func authorized(req *http.Request, body io.ReadCloser, token string) *http.Request {
	r := req.Clone(req.Context())
	r.Body = body
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// refusal turns a renewal that failed into what the request's caller can
// act on. An ended session reads as the 401 the server would have sent,
// so everything that already handles a rejected token handles it too; a
// rate limit or server error keeps its status, so it reads as the server
// being busy or failing rather than as being offline. Anything else, such
// as being offline, stays an error, as the request itself would have
// failed.
func refusal(req *http.Request, err error) (*http.Response, error) {
	var apiErr *api.Error
	switch {
	case errors.Is(err, ErrEnded):
		return synthesize(req, http.StatusUnauthorized, api.CodeUnauthorized, "your session ended; sign in again", 0), nil
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		return synthesize(req, apiErr.StatusCode, api.CodeRateLimited, apiErr.Message, apiErr.RetryAfter), nil
	case errors.As(err, &apiErr) && apiErr.StatusCode >= 500:
		return synthesize(req, apiErr.StatusCode, apiErr.Code, "could not renew the session: "+apiErr.Message, apiErr.RetryAfter), nil
	}
	return nil, err
}

func synthesize(req *http.Request, status int, code, msg string, wait time.Duration) *http.Response {
	b, _ := json.Marshal(map[string]string{"error": msg, "code": code})
	h := http.Header{"Content-Type": {"application/json"}}
	if wait > 0 {
		h.Set("Retry-After", strconv.Itoa(int(wait/time.Second)))
	}
	return &http.Response{
		StatusCode:    status,
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(string(b))),
		ContentLength: int64(len(b)),
		Request:       req,
	}
}

func closeBody(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}

// drain reads what is left of a response so its connection can be reused.
func drain(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	_ = res.Body.Close()
}
