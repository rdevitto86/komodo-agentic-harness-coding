package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

// Retry, size and timeout limits.
const (
	MaxAttempts = 4       // attempts per call, first try included
	MaxBody     = 8 << 20 // non-stream response body cap in bytes

	connectTimeout   = 10 * time.Second
	firstByteHosted  = 60 * time.Second
	firstByteLocal   = 300 * time.Second
	streamIdleHosted = 90 * time.Second
	streamIdleLocal  = 300 * time.Second
	backoffCeiling   = 30 * time.Second
	maxRetryAfter    = 60 * time.Second
	maxRedirects     = 10
	errBodyCap       = 64 << 10
)

// Options tunes one Client; every field has a working zero value.
type Options struct {
	CABundle     string                                     // PEM file added to the system roots
	Secrets      []string                                   // values scrubbed from logs and errors
	WarnQuotaPct float64                                    // warn when remaining falls below 100 minus this; 0 disables
	MaxBody      int64                                      // non-stream body cap in bytes; 0 means MaxBody
	Proxy        func(*http.Request) (*url.URL, error)      // proxy chooser; nil reads the environment
	Log          func(string)                               // receives redacted log lines
	OnWarn       func(Headroom)                             // called when headroom drops below the warn line
	Sleep        func(context.Context, time.Duration) error // retry sleep; nil waits on a timer
	Now          func() time.Time                           // clock; nil uses time.Now
	Int64N       func(int64) int64                          // jitter source in [0,n); nil uses math/rand/v2
}

// Endpoint describes what one caller may reach.
type Endpoint struct {
	Hosts      []string      // allowed hosts, with or without a port
	Methods    []string      // allowed methods; empty allows any
	SelfHosted bool          // allows plain http:// with a warning
	FirstByte  time.Duration // first-byte timeout; 0 picks the default
}

// Response is a finished call; Body is nil for streams.
type Response struct {
	Status   int
	Header   http.Header
	Body     []byte
	Attempts int
	Headroom []Headroom
}

// ErrRefused marks a call refused by policy before or during the request.
var ErrRefused = errors.New("refused")

// errFirstByte marks a response that never started in time.
var errFirstByte = errors.New("first byte timeout")

// epKey carries the Endpoint through a request context to the redirect check.
type epKey struct{}

// Client is the shared HTTP client; build one per process and reuse it.
type Client struct {
	opts Options
	hc   *http.Client
	red  *redactor
}

// sent is one successful response plus what the retry loop learned.
type sent struct {
	resp     *http.Response
	attempts int
	headroom []Headroom
}

// NewClient builds a Client from opts, loading the custom CA bundle when set.
func NewClient(opts Options) (*Client, error) {
	pool, err := rootPool(opts.CABundle)
	if err != nil {
		return nil, err
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = MaxBody
	}
	if opts.Sleep == nil {
		opts.Sleep = timerSleep
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Int64N == nil {
		opts.Int64N = rand.Int64N
	}
	if opts.Proxy == nil {
		opts.Proxy = http.ProxyFromEnvironment
	}
	c := &Client{opts: opts, red: &redactor{}}
	c.red.add(opts.Secrets...)
	tr := &http.Transport{
		Proxy:               c.proxyFor,
		DialContext:         (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: connectTimeout,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	c.hc = &http.Client{Transport: tr, CheckRedirect: c.checkRedirect}
	return c, nil
}

// rootPool returns the system roots plus the PEM bundle, or nil when no bundle is set.
func rootPool(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ca bundle: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("ca bundle %s holds no PEM certificates", path)
	}
	return pool, nil
}

// Do sends req with retries and returns the whole body, capped at the body limit.
func (c *Client) Do(ctx context.Context, ep Endpoint, req *http.Request) (*Response, error) {
	s, err := c.send(ctx, ep, req)
	if err != nil {
		return nil, c.wrapErr(err)
	}
	defer s.resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(s.resp.Body, c.opts.MaxBody+1))
	if err != nil {
		return nil, c.wrapErr(fmt.Errorf("read response body: %w", err))
	}
	if int64(len(body)) > c.opts.MaxBody {
		return nil, c.wrapErr(fmt.Errorf("response body exceeds %d bytes", c.opts.MaxBody))
	}
	return &Response{Status: s.resp.StatusCode, Header: s.resp.Header, Body: body, Attempts: s.attempts, Headroom: s.headroom}, nil
}

// send runs the attempt loop and returns the first 2xx response.
func (c *Client) send(ctx context.Context, ep Endpoint, req *http.Request) (*sent, error) {
	warn, err := c.vet(ep, req.URL, req.Method)
	if err != nil {
		return nil, err
	}
	if warn != "" {
		c.logf("%s", warn)
	}
	c.learn(req)
	ctx = context.WithValue(ctx, epKey{}, ep)
	attempts := MaxAttempts
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		attempts = 1
	}
	for attempt := 0; ; attempt++ {
		n := attempt + 1
		resp, err := c.once(ctx, ep, req)
		var wait time.Duration
		if err != nil {
			if !retryable(ctx, err) || n >= attempts {
				return nil, err
			}
			wait = backoff(attempt, c.opts.Int64N)
			c.logf("retry %d of %d for %s: %v; sleeping %s", n, attempts, c.target(req), err, wait)
		} else {
			hr := c.headroom(resp.Header)
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return &sent{resp: resp, attempts: n, headroom: hr}, nil
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyCap))
			resp.Body.Close()
			v := c.classify(resp.StatusCode, resp.Header, body)
			if v.limit != nil {
				return nil, v.limit
			}
			if !v.retry || n >= attempts {
				return nil, &StatusError{Status: resp.StatusCode, Header: resp.Header, Body: []byte(c.Redact(string(body))), Attempts: n}
			}
			wait = v.wait
			if !v.hasWait {
				wait = backoff(attempt, c.opts.Int64N)
			}
			c.logf("retry %d of %d for %s: status %d; sleeping %s", n, attempts, c.target(req), resp.StatusCode, wait)
		}
		if err := c.opts.Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// once makes a single attempt under the first-byte timer.
func (c *Client) once(ctx context.Context, ep Endpoint, req *http.Request) (*http.Response, error) {
	actx, cancel := context.WithCancel(ctx)
	r := req.Clone(actx)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("rewind request body: %w", err)
		}
		r.Body = body
	}
	var timedOut atomic.Bool
	fb := ep.FirstByteTimeout(req.URL)
	timer := time.AfterFunc(fb, func() {
		timedOut.Store(true)
		cancel()
	})
	resp, err := c.hc.Do(r)
	timer.Stop()
	if err != nil {
		cancel()
		if timedOut.Load() {
			return nil, fmt.Errorf("first byte not received within %s: %w", fb, errFirstByte)
		}
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelBody releases the attempt context when the body closes.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

// Close closes the body and cancels its attempt context.
func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// retryable reports whether err is a connection reset worth another attempt.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, ErrRefused) || errors.Is(err, errFirstByte) {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

// backoff picks a full-jitter sleep in [0, min(30 s, 1 s * 2^attempt)).
func backoff(attempt int, n func(int64) int64) time.Duration {
	ceil := backoffCeiling
	if attempt < 5 {
		ceil = min(ceil, time.Second<<attempt)
	}
	return time.Duration(n(int64(ceil)))
}

// timerSleep waits d or until ctx ends.
func timerSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// proxyFor picks the proxy for req and never proxies loopback.
func (c *Client) proxyFor(req *http.Request) (*url.URL, error) {
	if isLoopback(req.URL.Hostname()) {
		return nil, nil
	}
	return c.opts.Proxy(req)
}

// checkRedirect follows a redirect only to the original host and policy.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if canonicalHost(req.URL) != canonicalHost(via[0].URL) {
		return fmt.Errorf("%w: redirect from %s to %s", ErrRefused, via[0].URL.Host, req.URL.Host)
	}
	ep, _ := req.Context().Value(epKey{}).(Endpoint)
	_, err := c.vet(ep, req.URL, req.Method)
	return err
}

// FirstByteTimeout is how long the first response byte may take.
func (e Endpoint) FirstByteTimeout(u *url.URL) time.Duration {
	switch {
	case e.FirstByte > 0:
		return e.FirstByte
	case isLoopback(u.Hostname()):
		return firstByteLocal
	}
	return firstByteHosted
}

// StreamIdle is the silence after which a stream counts as dead.
func (e Endpoint) StreamIdle(u *url.URL) time.Duration {
	if e.SelfHosted || isLoopback(u.Hostname()) {
		return streamIdleLocal
	}
	return streamIdleHosted
}
