package lease

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// rec builds a held record for the registry tests.
func rec(group string) Lease {
	return Lease{
		Repo: testRepo, Group: group, Run: "run-" + group, Branch: "komodo/ep-1-" + group, Worktree: "/wt/" + group,
		PID: 1, Host: "h1", Fence: 1, State: StateHeld, TakenAt: testStart, RenewedAt: testStart,
	}
}

// age sets a file's modification time to d ago.
func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryCreateGetSwapDelete(t *testing.T) {
	ctx := context.Background()
	r := NewFileRegistry(filepath.Join(t.TempDir(), "leases"), DefaultLockTimeout)

	if _, err := r.Get(ctx, testRepo, "g1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on empty: %v", err)
	}
	if err := r.Create(ctx, rec("g1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.Create(ctx, rec("g1")); !errors.Is(err, ErrExists) {
		t.Fatalf("second Create: %v", err)
	}
	got, err := r.Get(ctx, testRepo, "g1")
	if err != nil || got.Rev != 1 || got.Run != "run-g1" {
		t.Fatalf("Get after Create: %+v %v", got, err)
	}

	next := got
	next.Run = "run-new"
	rev, err := r.Swap(ctx, next, 1)
	if err != nil || rev != 2 {
		t.Fatalf("Swap: rev %d err %v", rev, err)
	}
	if _, err := r.Swap(ctx, next, 1); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("Swap with a stale rev: %v", err)
	}
	if got, _ = r.Get(ctx, testRepo, "g1"); got.Rev != 2 || got.Run != "run-new" {
		t.Fatalf("Get after Swap: %+v", got)
	}
	if _, err := r.Swap(ctx, rec("none"), 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Swap on absent: %v", err)
	}

	if err := r.Delete(ctx, testRepo, "g1", 1); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("Delete with a stale rev: %v", err)
	}
	if err := r.Delete(ctx, testRepo, "g1", 2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := r.Delete(ctx, testRepo, "g1", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete on absent: %v", err)
	}
	if err := r.Create(ctx, rec("g1")); err != nil {
		t.Fatalf("Create after Delete: %v", err)
	}
}

func TestRegistryFilesOnDisk(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "leases")
	r := NewFileRegistry(root, DefaultLockTimeout)
	if err := r.Create(ctx, rec("g1")); err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir(root)
	if err != nil || len(dirs) != 1 || !dirs[0].IsDir() {
		t.Fatalf("a repo id with slashes must map to one folder: %v %v", dirs, err)
	}
	files, _ := os.ReadDir(filepath.Join(root, dirs[0].Name()))
	if len(files) != 2 || files[0].Name() != "g1.json" || files[1].Name() != "g1.lock" {
		t.Fatalf("after a write the record and its free lock file stay, with no temp file: %v", files)
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, ".hidden"} {
		if err := r.Create(ctx, rec(bad)); !errors.Is(err, ErrBadKey) {
			t.Errorf("group %q: %v", bad, err)
		}
	}
	badRepo := rec("g2")
	badRepo.Repo = ""
	if err := r.Create(ctx, badRepo); !errors.Is(err, ErrBadKey) {
		t.Errorf("empty repo: %v", err)
	}
	for _, repo := range []string{"..", "."} {
		l := rec("g2")
		l.Repo = repo
		if err := r.Create(ctx, l); err != nil {
			t.Errorf("repo %q: %v", repo, err)
		}
	}
	// A repo id of dots is escaped into a folder inside the root, never read as a path.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "g2.json")); err == nil {
		t.Fatal("a repo id of .. escaped the registry folder")
	}
	if dirs, _ := os.ReadDir(root); len(dirs) != 3 {
		t.Fatalf("want 3 folders under the root, got %v", dirs)
	}
}

func TestRegistryList(t *testing.T) {
	ctx := context.Background()
	r := NewFileRegistry(filepath.Join(t.TempDir(), "leases"), DefaultLockTimeout)
	if got, err := r.List(ctx, testRepo); err != nil || len(got) != 0 {
		t.Fatalf("List of an absent repo: %v %v", got, err)
	}
	for _, g := range []string{"b", "a"} {
		if err := r.Create(ctx, rec(g)); err != nil {
			t.Fatal(err)
		}
	}
	other := rec("c")
	other.Repo = "github.com/acme/other"
	if err := r.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(recordFile(t, r, testRepo, "a"))
	for _, junk := range []string{"a.lock", ".a.json.tmp-123", "a.lock.stale-1-2", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, junk), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.List(ctx, testRepo)
	if err != nil || len(got) != 2 || got[0].Group != "a" || got[1].Group != "b" {
		t.Fatalf("List: %+v %v", got, err)
	}
}

func TestRegistryCorruptRecord(t *testing.T) {
	ctx := context.Background()
	r := NewFileRegistry(filepath.Join(t.TempDir(), "leases"), DefaultLockTimeout)
	if err := r.Create(ctx, rec("g1")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordFile(t, r, testRepo, "g1"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, testRepo, "g1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a corrupt record must be an error, not absence: %v", err)
	}
}

// holdLock takes the operating-system lock a registry writer would take and ages the lock file.
func holdLock(t *testing.T, lock string) func() error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := core.FileLock(context.Background(), lock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	age(t, lock, 48*time.Hour)
	return unlock
}

// A held lock keeps a writer waiting however old the lock file looks, and the writer goes ahead once it is freed.
func TestHeldLockMakesWriterWait(t *testing.T) {
	ctx := context.Background()
	r := NewFileRegistry(filepath.Join(t.TempDir(), "leases"), 30*time.Second)
	unlock := holdLock(t, lockFile(t, r, testRepo, "g1"))
	done := make(chan error, 1)
	go func() { done <- r.Create(ctx, rec("g1")) }()
	select {
	case err := <-done:
		t.Fatalf("the writer did not wait for a held lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(recordFile(t, r, testRepo, "g1")); err == nil {
		t.Fatal("record written under a held lock")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Create after the lock was freed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never proceeded")
	}
}

// A lock held past the timeout gives ErrLocked, and the timeout is the whole wait.
func TestLockTimeoutIsTheLongestWait(t *testing.T) {
	r := NewFileRegistry(filepath.Join(t.TempDir(), "leases"), 200*time.Millisecond)
	unlock := holdLock(t, lockFile(t, r, testRepo, "g1"))
	defer func() { _ = unlock() }()
	start := time.Now()
	err := r.Create(context.Background(), rec("g1"))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("waited %v, want about 200ms", d)
	}
}

func TestWaitStopsOnContext(t *testing.T) {
	r := NewFileRegistry(filepath.Join(t.TempDir(), "leases"), time.Hour)
	unlock := holdLock(t, lockFile(t, r, testRepo, "g1"))
	defer func() { _ = unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := r.Create(ctx, rec("g1")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context error, got %v", err)
	}
}

// Many writers cycling through one lock are never inside together.
func TestLockedWritersNeverOverlap(t *testing.T) {
	ctx := context.Background()
	r := NewFileRegistry(filepath.Join(t.TempDir(), "leases"), 30*time.Second)
	lock := lockFile(t, r, testRepo, "g1")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	var inside, overlaps, entered atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 8; k++ {
				unlock, err := r.lock(ctx, lock)
				if err != nil {
					t.Error(err)
					return
				}
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				entered.Add(1)
				time.Sleep(time.Millisecond)
				inside.Add(-1)
				unlock()
			}
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 || entered.Load() != 128 {
		t.Fatalf("overlaps %d entered %d", overlaps.Load(), entered.Load())
	}
}

// TestCrashHelper is the child process of TestWriteCrash; it dies at the stage in KOMODO_LEASE_CRASH_AT.
func TestCrashHelper(t *testing.T) {
	stage := os.Getenv("KOMODO_LEASE_CRASH_AT")
	if stage == "" {
		t.Skip("child of TestWriteCrash")
	}
	r := NewFileRegistry(os.Getenv("KOMODO_LEASE_ROOT"), DefaultLockTimeout)
	crashHook = func(s string) {
		if s == stage {
			os.Exit(7)
		}
	}
	got, err := r.Get(context.Background(), testRepo, "g1")
	if err != nil {
		t.Fatal(err)
	}
	got.Run = "run-crashed"
	_, _ = r.Swap(context.Background(), got, got.Rev)
	t.Fatal("the crash hook did not fire")
}

func TestWriteCrash(t *testing.T) {
	for _, stage := range []string{"after_lock", "after_rename"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "leases")
			r := NewFileRegistry(root, 30*time.Second)
			if err := r.Create(ctx, rec("g1")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
			cmd.Env = append(os.Environ(), "KOMODO_LEASE_CRASH_AT="+stage, "KOMODO_LEASE_ROOT="+root)
			var ee *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &ee) || ee.ExitCode() != 7 {
				t.Fatalf("child did not die at %s: %v", stage, err)
			}
			lock := lockFile(t, r, testRepo, "g1")
			if _, err := os.Stat(lock); err != nil {
				t.Fatalf("the dead writer leaves its lock file: %v", err)
			}
			got, err := r.Get(ctx, testRepo, "g1")
			if err != nil {
				t.Fatalf("record must be whole: %v", err)
			}
			wantRun, wantRev := "run-g1", 1
			if stage == "after_rename" {
				wantRun, wantRev = "run-crashed", 2
			}
			if got.Run != wantRun || got.Rev != wantRev {
				t.Fatalf("record %+v", got)
			}
			// The kernel freed the dead writer's lock, so the next writer goes ahead at once.
			got.Run = "run-next"
			start := time.Now()
			if _, err := r.Swap(ctx, got, got.Rev); err != nil {
				t.Fatalf("Swap after the writer died: %v", err)
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("the next writer waited %v", d)
			}
		})
	}
}
