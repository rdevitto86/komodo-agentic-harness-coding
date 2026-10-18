package core

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LimitKind says why a call was returned as a limit.
type LimitKind int

// Limit kinds.
const (
	LimitSpend      LimitKind = iota + 1 // monthly spend cap reached
	LimitQuota                           // quota error in the body
	LimitRetryAfter                      // retry-after over 60 s
)

// LimitError is a call returned as a limit instead of retried.
type LimitError struct {
	Kind       LimitKind
	Status     int
	RetryAfter time.Duration
	Code       string
}

// Error describes the limit that stopped the call.
func (e *LimitError) Error() string {
	switch e.Kind {
	case LimitSpend:
		return fmt.Sprintf("spend limit reached (HTTP %d)", e.Status)
	case LimitQuota:
		return fmt.Sprintf("quota exhausted: %s (HTTP %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("rate limited for %s (HTTP %d)", e.RetryAfter, e.Status)
}

// StatusError is a final non-2xx response with its redacted body.
type StatusError struct {
	Status   int
	Header   http.Header
	Body     []byte
	Attempts int
}

// Error describes the status and how many attempts were made.
func (e *StatusError) Error() string {
	snippet := string(e.Body)
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	return fmt.Sprintf("unexpected status %d after %d attempts: %s", e.Status, e.Attempts, snippet)
}

// Headroom is one rate-limit bucket read from response headers.
type Headroom struct {
	Name      string
	Limit     int64
	Remaining int64
	Reset     time.Time
}

// verdict is how a non-2xx response is handled.
type verdict struct {
	limit   *LimitError
	retry   bool
	wait    time.Duration
	hasWait bool
}

const spendCode = "enforced_spend_limit_reached"

// redactor scrubs registered secret values from text.
type redactor struct {
	mu   sync.RWMutex
	vals []string
}

// add registers values, longest first so overlaps scrub whole.
func (r *redactor) add(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		if v != "" && !slices.Contains(r.vals, v) {
			r.vals = append(r.vals, v)
		}
	}
	sort.Slice(r.vals, func(i, j int) bool { return len(r.vals[i]) > len(r.vals[j]) })
}

// apply replaces every registered value in s.
func (r *redactor) apply(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vals {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	return s
}

// redactedError keeps the wrapped chain but prints scrubbed text.
type redactedError struct {
	msg string
	err error
}

// Error returns the scrubbed message.
func (e *redactedError) Error() string { return e.msg }

// Unwrap exposes the original error for errors.Is and errors.As.
func (e *redactedError) Unwrap() error { return e.err }

// Redact scrubs registered secrets from s.
func (c *Client) Redact(s string) string { return c.red.apply(s) }

// AddSecrets registers more values to scrub, such as env: sourced keys.
func (c *Client) AddSecrets(vals ...string) { c.red.add(vals...) }

// classify decides whether a non-2xx response is a limit, a retry or final.
func (c *Client) classify(status int, h http.Header, body []byte) verdict {
	const maxRetryAfter = 60 * time.Second

	ra, hasRA := parseRetryAfter(h.Get("Retry-After"), c.opts.Now())

	if status == http.StatusTooManyRequests {
		code := errorCode(body)
		switch {
		case code == spendCode:
			return verdict{limit: &LimitError{Kind: LimitSpend, Status: status, Code: code}}
		case strings.Contains(code, "quota"):
			return verdict{limit: &LimitError{Kind: LimitQuota, Status: status, Code: code}}
		case hasRA && ra > maxRetryAfter:
			return verdict{limit: &LimitError{Kind: LimitRetryAfter, Status: status, RetryAfter: ra}}
		}

		return verdict{retry: true, wait: ra, hasWait: hasRA}
	}

	switch status {
	case 408, 500, 502, 503, 504, 529:
		return verdict{retry: true, wait: min(ra, maxRetryAfter), hasWait: hasRA}
	}

	return verdict{}
}

// headroom reads the rate-limit buckets and fires the warning when one runs low.
func (c *Client) headroom(h http.Header) []Headroom {
	const headroomPrefix = "Anthropic-Ratelimit-"

	var out []Headroom

	for k := range h {
		if !strings.HasPrefix(k, headroomPrefix) || !strings.HasSuffix(k, "-Remaining") {
			continue
		}

		name := strings.TrimSuffix(strings.TrimPrefix(k, headroomPrefix), "-Remaining")

		rem, err := strconv.ParseInt(h.Get(k), 10, 64)
		if err != nil {
			continue
		}

		hr := Headroom{Name: strings.ToLower(name), Remaining: rem}
		hr.Limit, _ = strconv.ParseInt(h.Get(headroomPrefix+name+"-Limit"), 10, 64)
		hr.Reset, _ = time.Parse(time.RFC3339, h.Get(headroomPrefix+name+"-Reset"))
		out = append(out, hr)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if c.opts.WarnQuotaPct > 0 && c.opts.OnWarn != nil {
		for _, hr := range out {
			if hr.Limit > 0 && float64(hr.Remaining)*100 < (100-c.opts.WarnQuotaPct)*float64(hr.Limit) {
				c.opts.OnWarn(hr)
			}
		}
	}

	return out
}

// vet checks scheme, host and method against the endpoint and returns any warning.
func (c *Client) vet(ep Endpoint, u *url.URL, method string) (string, error) {
	var warn string

	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			if !ep.SelfHosted {
				return "", fmt.Errorf("%w: plain http to %s needs a loopback or self_hosted endpoint", ErrRefused, u.Host)
			}
			warn = fmt.Sprintf("⚠️ plain http to self-hosted endpoint %s: traffic is not encrypted", u.Host)
		}
	default:
		return "", fmt.Errorf("%w: scheme %q is not http or https", ErrRefused, u.Scheme)
	}

	if !hostAllowed(ep.Hosts, u) {
		return "", fmt.Errorf("%w: host %s is not on the endpoint allowlist", ErrRefused, u.Host)
	}
	if len(ep.Methods) > 0 && !slices.ContainsFunc(
		ep.Methods,
		func(m string) bool { return strings.EqualFold(m, method) },
	) {
		return "", fmt.Errorf("%w: method %s is not allowed for this endpoint", ErrRefused, method)
	}

	return warn, nil
}

// learn registers the credentials a request carries so they can never be logged.
func (c *Client) learn(req *http.Request) {
	for _, name := range []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "Api-Key"} {
		for _, v := range req.Header.Values(name) {
			c.red.add(v)
			if i := strings.LastIndexByte(v, ' '); i >= 0 {
				c.red.add(v[i+1:])
			}
		}
	}
}

// wrapErr scrubs secrets from err's text while keeping its type chain.
func (c *Client) wrapErr(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{msg: c.Redact(err.Error()), err: err}
}

// logf writes one scrubbed log line.
func (c *Client) logf(format string, args ...any) {
	if c.opts.Log != nil {
		c.opts.Log(c.Redact(fmt.Sprintf(format, args...)))
	}
}

// target names a request for logs without its query string.
func (c *Client) target(req *http.Request) string {
	return req.Method + " " + req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
}

// errorCode pulls the provider error code out of a JSON error body.
func errorCode(body []byte) string {
	var v struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Details struct {
				ErrorCode string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
	}

	_ = json.Unmarshal(body, &v)
	for _, s := range []string{v.Error.Details.ErrorCode, v.Error.Code, v.Error.Type} {
		if s == spendCode || strings.Contains(s, "quota") {
			return s
		}
	}

	return ""
}

// parseRetryAfter reads a Retry-After header as seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs >= 0 {
		if secs > int64(24*time.Hour/time.Second) {
			secs = int64(24 * time.Hour / time.Second)
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}

	return 0, false
}

// hostAllowed matches u against allowlist entries given as host or host:port.
func hostAllowed(hosts []string, u *url.URL) bool {
	full, name := canonicalHost(u), strings.ToLower(u.Hostname())
	for _, e := range hosts {
		e = strings.ToLower(strings.TrimSpace(e))
		if host, port, err := net.SplitHostPort(e); err == nil {
			if canonicalEntry(host, port, u.Scheme) == full || e == strings.ToLower(u.Host) {
				return true
			}
			continue
		}
		if strings.Trim(e, "[]") == name {
			return true
		}
	}

	return false
}

// canonicalHost lowercases the host and drops the scheme's default port.
func canonicalHost(u *url.URL) string {
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "http" && u.Port() == "80", u.Scheme == "https" && u.Port() == "443":
		return bracketV6(strings.ToLower(u.Hostname()))
	}
	return host
}

// bracketV6 wraps an IPv6 literal in brackets and leaves other hosts alone.
func bracketV6(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// canonicalEntry joins an allowlist host and port the way canonicalHost prints a URL host.
func canonicalEntry(host, port, scheme string) string {
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		return bracketV6(host)
	}
	return net.JoinHostPort(host, port)
}

// isLoopback reports whether host is localhost or a loopback address.
func isLoopback(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
