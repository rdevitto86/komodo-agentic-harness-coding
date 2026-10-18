package sandbox

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"weak"
)

// launch describes one wrapped process.
type launch struct {
	Timeout time.Duration
	Bin     string
	Args    []string
	Env     []string // nil keeps the host's environment
	Dir     string
	Marker  string // NAME=value from newMarker
	// kills what a cancel cannot reach from the host, such as a process inside a container
	Remote func()
	Reap   bool // the command runs under a shell that kills its whole sandbox on SIGTERM
}

// procInfo is one row of the process table.
type procInfo struct {
	pid, ppid, pgid int
	line            string // the whole row, which on a tagged process holds its environment
}

// releases holds the timeout release of each running command, keyed weakly so an abandoned command does not stay alive.
var releases sync.Map

// psTable returns the process table with each process's environment; tests replace it.
var psTable = func() (string, error) {
	flags := "-axwwe"
	if runtime.GOOS == "darwin" {
		flags = "-axwwE"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", flags, "-o", "pid=,ppid=,pgid=,command=").Output()
	return string(out), err
}

// Wait waits for a command from Manager.Command and then releases its timeout.
func Wait(cmd *exec.Cmd) error {
	err := cmd.Wait()
	release(weak.Make(cmd))
	return err
}

// newMarker returns a fresh NAME=value tag for one command.
func newMarker() string {
	// markerKey names the variable that tags every process a sandboxed command starts, so a descendant that left the process
	// group is still found.
	const markerKey = "KOMODO_SANDBOX_CMD"
	var b [8]byte
	_, _ = rand.Read(b[:])
	return markerKey + "=" + hex.EncodeToString(b[:])
}

// confined builds the process for one wrapped command: its own process group, a cancel that kills the whole tree, and the
// timeout. A command with a timeout holds a context that Wait releases.
func confined(ctx context.Context, l launch) *exec.Cmd {
	// reapWait is how long a sandbox's own reaper gets to kill every process of its sandbox before the host kills what is
	// left.
	const reapWait = 200 * time.Millisecond

	cancel := context.CancelFunc(func() {})
	if l.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, l.Timeout)
	}

	cmd := exec.CommandContext(ctx, l.Bin, l.Args...)
	cmd.Dir = l.Dir

	env := l.Env
	if env == nil {
		env = os.Environ()
	}

	cmd.Env = append(slices.Clone(env), l.Marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			if l.Reap {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				time.Sleep(reapWait)
			}
			killTree(cmd.Process.Pid, l.Marker)
		}
		if l.Remote != nil {
			l.Remote()
		}

		return nil
	}

	cmd.WaitDelay = 5 * time.Second
	if l.Timeout > 0 {
		track(cmd, cancel)
	} else {
		cancel()
	}

	return cmd
}

// killTree kills the command's process group and then, in rounds, every process that descends from it or carries its marker,
// so a child that started its own session dies too.
func killTree(root int, marker string) {
	_ = syscall.Kill(-root, syscall.SIGKILL)
	self := os.Getpid()

	for round := 0; round < 5; round++ {
		out, err := psTable()
		if err != nil {
			_ = syscall.Kill(root, syscall.SIGKILL)
			return
		}

		killed := 0
		for _, pid := range selectVictims(parseProcs(out), root, marker) {
			if pid != self && pid > 1 && syscall.Kill(pid, syscall.SIGKILL) == nil {
				killed++
			}
		}
		if killed == 0 {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// selectVictims returns the root, its process group, every process carrying the marker, and all descendants of those.
func selectVictims(table []procInfo, root int, marker string) []int {
	in := map[int]bool{root: true}
	for _, p := range table {
		if p.pgid == root || strings.Contains(p.line, marker) {
			in[p.pid] = true
		}
	}

	for grew := true; grew; {
		grew = false
		for _, p := range table {
			if !in[p.pid] && in[p.ppid] {
				in[p.pid], grew = true, true
			}
		}
	}

	out := make([]int, 0, len(in))
	for pid := range in {
		out = append(out, pid)
	}

	return out
}

// parseProcs reads the rows of "pid ppid pgid command..." and skips lines that are not rows.
func parseProcs(out string) []procInfo {
	var rows []procInfo

	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)

	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}

		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])

		pgid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}

		rows = append(rows, procInfo{pid: pid, ppid: ppid, pgid: pgid, line: strings.TrimSpace(sc.Text())})
	}

	return rows
}

// track remembers a command's release and frees it if the command is dropped without Wait.
func track(cmd *exec.Cmd, cancel context.CancelFunc) {
	key := weak.Make(cmd)
	releases.Store(key, cancel)
	runtime.AddCleanup(cmd, func(k weak.Pointer[exec.Cmd]) { release(k) }, key)
}

// release runs and forgets the release stored under key.
func release(key weak.Pointer[exec.Cmd]) {
	if v, ok := releases.LoadAndDelete(key); ok {
		v.(context.CancelFunc)()
	}
}
