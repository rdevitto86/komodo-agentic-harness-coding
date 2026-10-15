package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// Limits on what a tool puts in front of the model.
const (
	exitRoom    = 64               // bytes of a run reply kept free for its exit line
	replyCap    = 4096             // bytes of a reply, except read
	headKeep    = 1024             // bytes kept from the start of a capped reply
	readLines   = 2000             // lines one read call returns
	maxLogBytes = 64 << 20         // bytes of one command's output kept in its log
	runTimeout  = 10 * time.Minute // a run command is killed with its whole tree after this
	hostTimeout = 2 * time.Minute  // a host git call is killed after this
)

// capText keeps the first headKeep and the last bytes of s within replyCap, joined by one line naming the log.
func capText(s, logPath string) string {
	if len(s) <= replyCap {
		return s
	}
	return capParts(s[:headKeep], s[len(s)-replyCap:], len(s), logPath, replyCap)
}

// capParts joins the head and the end of an output of total bytes with the line that names its log; the result stays within budget bytes.
func capParts(head, tail string, total int, logPath string, budget int) string {
	room := budget - headKeep - len(fmt.Sprintf("\n[… %d bytes cut; full output in %s]\n", total, logPath))
	if room < 256 {
		room = 256
	}
	if len(head) > headKeep {
		head = head[:headKeep]
	}
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	if len(tail) > room {
		tail = tail[len(tail)-room:]
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	cut := total - len(head) - len(tail)
	return head + fmt.Sprintf("\n[… %d bytes cut; full output in %s]\n", cut, logPath) + tail
}

// capture collects a command's combined output: the first replyCap bytes in memory, and the whole output in a log file once it is longer.
type capture struct {
	mu      sync.Mutex
	logPath string
	limit   int // bytes of reply kept, which leaves room for the exit line
	buf     bytes.Buffer
	f       *os.File
	total   int64
	failed  error
}

// Write stores output, opening the log file when the output outgrows the reply.
func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.total += int64(n)
	if c.f == nil && c.buf.Len()+n <= c.limit {
		c.buf.Write(p)
		return n, nil
	}
	if c.f == nil {
		if err := os.MkdirAll(filepath.Dir(c.logPath), 0o755); err == nil {
			c.f, c.failed = os.Create(c.logPath)
		} else {
			c.failed = err
		}
		if c.f != nil {
			_, _ = c.f.Write(c.buf.Bytes())
		}
	}
	if c.f != nil && c.total <= maxLogBytes {
		_, _ = c.f.Write(p)
	}
	return n, nil
}

// reply builds the capped reply of what was collected and closes the log.
func (c *capture) reply() Reply {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f == nil {
		return Reply{Text: c.buf.String()}
	}
	defer func() { _ = c.f.Close() }()
	size := min(c.total, maxLogBytes)
	head := make([]byte, headKeep)
	nh, _ := c.f.ReadAt(head, 0)
	tail := make([]byte, c.limit)
	off := max(size-int64(len(tail)), 0)
	nt, _ := c.f.ReadAt(tail, off)
	if c.total > maxLogBytes {
		_, _ = c.f.WriteAt([]byte(fmt.Sprintf("\n[log stopped at %d bytes]\n", maxLogBytes)), size)
	}
	return Reply{Text: capParts(string(head[:nh]), string(tail[:nt]), int(c.total), c.logPath, c.limit), LogPath: c.logPath}
}

// outcome of a command that ran.
type runResult struct {
	Reply    Reply
	Exit     int
	TimedOut bool
	StartErr error
}

// runConfined starts a command from the sandbox, waits for it with the sandbox's own wait and collects its output.
func runConfined(ctx context.Context, cmd *exec.Cmd, logPath string) runResult {
	out := &capture{logPath: logPath, limit: replyCap - exitRoom}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return runResult{StartErr: err}
	}
	err := sandbox.Wait(cmd)
	res := runResult{Reply: out.reply(), Exit: cmd.ProcessState.ExitCode()}
	res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	if err != nil && cmd.ProcessState == nil {
		res.StartErr = err
	}
	return res
}

// hostOutput runs a host command in its own process group with a timeout and returns its standard output.
// The group is killed whole when the time runs out or the context ends.
func hostOutput(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	killGroupOnCancel(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &limitedWriter{w: &stderr, left: 8 << 10}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("%s: %s", argv[1], msg)
	}
	return stdout.Bytes(), nil
}

// limitedWriter passes on at most left bytes and drops the rest.
type limitedWriter struct {
	w    io.Writer
	left int
}

// Write forwards what still fits.
func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.left > 0 {
		n := min(len(p), l.left)
		_, _ = l.w.Write(p[:n])
		l.left -= n
	}
	return len(p), nil
}

// taskSource names this package in a fault.
var taskSource = fault.Source{Component: "task"}

// stderrPrinter shows a fault on standard error; standard output belongs to the tool server's protocol.
type stderrPrinter struct{}

// PrintError writes the fault's reason on one line.
func (stderrPrinter) PrintError(f fault.Fault) error {
	_, err := fmt.Fprintf(os.Stderr, "komodo: %s\n", f.Reason)
	return err
}

// guardian turns a panic of a goroutine this package starts into a published fault.
var guardian = fault.New(nil, stderrPrinter{})
