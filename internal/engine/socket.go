package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
)

// maxFrame is the longest request line the engine reads.
const maxFrame = 4 << 20

// connWriter writes reply lines to one connection, one at a time.
type connWriter struct {
	mu sync.Mutex
	c  net.Conn
}

// send writes one reply as a line.
func (w *connWriter) send(r Reply) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.c.Write(append(b, '\n'))
	return err
}

// ConnPrinter prints a fault as a Reply with Status "error" on the connection that is handling a command; it implements fault.Printer.
type ConnPrinter struct {
	mu   sync.Mutex
	open []printTarget // the connections with a command in flight, oldest first
}

// printTarget is a connection and the request whose handling a fault would belong to.
type printTarget struct {
	w  *connWriter
	id string
}

// attach marks a request as in flight on a connection and returns the call that ends it.
func (p *ConnPrinter) attach(w *connWriter, id string) func() {
	t := printTarget{w, id}
	p.mu.Lock()
	p.open = append(p.open, t)
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for i := len(p.open) - 1; i >= 0; i-- {
			if p.open[i] == t {
				p.open = append(p.open[:i], p.open[i+1:]...)
				return
			}
		}
	}
}

// PrintError writes the fault to the connection of the newest command in flight; a fault with no connection attached is not printed.
func (p *ConnPrinter) PrintError(f fault.Fault) error {
	p.mu.Lock()
	if len(p.open) == 0 {
		p.mu.Unlock()
		return nil
	}
	t := p.open[len(p.open)-1]
	p.mu.Unlock()
	return t.w.send(Reply{ID: t.id, Status: StatusError, Code: f.Code, Detail: f.Reason})
}

// serveConn reads request lines from one connection, answers each in order and ends when the client leaves.
func (e *Engine) serveConn(ctx context.Context, c net.Conn) {
	defer e.closeConn(c)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &connWriter{c: c}
	lines := make(chan []byte)
	e.m.spawn(func() {
		defer close(lines)
		rd := bufio.NewReaderSize(c, 64<<10)
		for {
			b, err := rd.ReadBytes('\n')
			if len(b) > maxFrame {
				b = nil
			}
			if len(b) > 1 {
				select {
				case lines <- b:
				case <-cctx.Done():
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	})
	for b := range lines {
		var req Request
		if err := json.Unmarshal(b, &req); err != nil {
			_ = w.send(Reply{ID: scrapeID(b), Status: StatusError, Code: CodeBadFrame, Detail: "the line is not a valid request: " + err.Error(), Final: true})
			continue
		}
		done := e.printer.attach(w, req.ID)
		e.guarded(w, req.ID, func() { e.handle(cctx, w, req) })
		done()
	}
}

// guarded runs one request handler and turns a panic into an error reply, so one bad request never ends the engine.
func (e *Engine) guarded(w *connWriter, id string, fn func()) {
	defer func() {
		if p := recover(); p != nil {
			e.m.reportErr("", "", fmt.Errorf("handle request %s: panic: %v", id, p))
			_ = w.send(Reply{ID: id, Status: StatusError, Code: "internal", Detail: "the engine failed while handling the request", Final: true})
		}
	}()
	fn()
}

// scrapeID returns the id of a frame that did not decode, when it has one.
func scrapeID(b []byte) string {
	var s struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &s)
	return s.ID
}

// closeConn closes a connection and runs the exit check.
func (e *Engine) closeConn(c net.Conn) {
	_ = c.Close()
	e.mu.Lock()
	delete(e.conns, c)
	e.mu.Unlock()
	e.checkExit()
}

// Conn is a client connection to a running engine.
type Conn struct {
	c  net.Conn
	rd *bufio.Reader
}

// ErrNoEngine marks a socket that nothing answers on.
var ErrNoEngine = errors.New("no engine answers on the control socket")

// Dial connects to a running engine and returns ErrNoEngine when nothing answers.
func Dial(socketPath string) (*Conn, error) {
	c, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrNoEngine
		}
		return nil, err
	}
	return &Conn{c: c, rd: bufio.NewReaderSize(c, 64<<10)}, nil
}

// Send writes one request line.
func (c *Conn) Send(r Request) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = c.c.Write(append(b, '\n'))
	return err
}

// SendRaw writes one line as it is.
func (c *Conn) SendRaw(line []byte) error {
	_, err := c.c.Write(append(line, '\n'))
	return err
}

// Recv reads the next reply line.
func (c *Conn) Recv() (Reply, error) {
	b, err := c.rd.ReadBytes('\n')
	if err != nil && (len(b) == 0 || !errors.Is(err, io.EOF)) {
		return Reply{}, err
	}
	var r Reply
	if err := json.Unmarshal(b, &r); err != nil {
		return Reply{}, err
	}
	return r, nil
}

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }
