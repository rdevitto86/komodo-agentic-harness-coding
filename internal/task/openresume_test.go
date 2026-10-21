package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// openRig is a ClaudeCLI rig whose one task is checked by the command "fail", which a test flips to passing.
func openRig(t *testing.T, lim Limits, extra func(*Collaborators)) *pipeRig {
	t.Helper()
	r := newPipeRig(t, rigOpts{
		chain:  []string{string(llm.ClaudeCLI)},
		limits: lim,
		mut: func(s *GroupSpec, c *Collaborators) {
			s.Tasks[0].DoneWhen = "fail"
			if extra != nil {
				extra(c)
			}
		},
	})
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	return r
}

// againResult is the builder's done result for a resume.
func againResult(n int) func(llm.Session) (llm.Stream, error) {
	return sayResult(`{"status":"done","summary":"again ` + strings.Repeat("x", n) + `",` +
		`"evidence":[{"cmd":"go test ./a/...","exit":0,"excerpt":"ok  a 0.01s"}]}`)
}

func TestADoneResultWithAnOpenTaskResumesTheSessionAndSpendsNoTry(t *testing.T) {
	r := openRig(t, Limits{}, nil)
	setCheck(r, "boom-output", "1")

	var texts []string
	r.model.resume = func(_ llm.Session, in llm.Input) (llm.Stream, error) {
		texts = append(texts, in.Text)
		if len(texts) == 3 {
			r.m.c.Commands["fail"] = []string{"true"}
		}
		return againResult(len(texts))(llm.Session{})
	}
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.reachCleanup()

	if len(texts) != 3 || !strings.Contains(texts[0], "boom-output") || !strings.Contains(texts[0], "t1") {
		t.Errorf("resumes %q, want 3 carrying the failing check output", texts)
	}
	if got := r.model.starts["builder"]; got != 1 {
		t.Errorf("%d builder sessions, want 1: the same session is resumed", got)
	}
	if got := r.rt.progress().RetriesLeft; got != 3 {
		t.Errorf("retries left %d, want 3: an open task spends no try", got)
	}
}

func TestResumesStopWhenTheOriginalWindowEndsAndSpendOneTry(t *testing.T) {
	var (
		mu  sync.Mutex
		cur = time.Unix(1_000_000, 0)
	)
	r := openRig(t, Limits{SessionWall: time.Hour}, func(c *Collaborators) {
		c.Now = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return cur
		}
	})
	setCheck(r, "boom", "1")

	resumed := 0
	r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) {
		resumed++
		mu.Lock()
		cur = cur.Add(20 * time.Minute)
		mu.Unlock()
		return againResult(resumed)(llm.Session{})
	}
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		func(s llm.Session) (llm.Stream, error) {
			r.m.c.Commands["fail"] = []string{"true"}
			return building(r.wt, "a.go", "package a\n", builderDone)(s)
		})
	r.reachCleanup()

	if resumed != 3 {
		t.Errorf("%d resumes, want 3: the 1 hour window is not renewed by a resume", resumed)
	}
	if got := r.model.starts["builder"]; got != 2 {
		t.Errorf("%d builder sessions, want 2", got)
	}
	if got := r.rt.progress().RetriesLeft; got != 2 {
		t.Errorf("retries left %d, want 2: the window ending spends exactly one try", got)
	}
}

func TestADoneResultThatCannotResumeSpendsOneTryAsBefore(t *testing.T) {
	r := openRig(t, Limits{}, nil)
	setCheck(r, "boom", "1")
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		func(s llm.Session) (llm.Stream, error) {
			r.m.c.Commands["fail"] = []string{"true"}
			return building(r.wt, "a.go", "package a\n", builderDone)(s)
		})
	r.reachCleanup()

	if got := r.rt.progress().RetriesLeft; got != 2 {
		t.Errorf("retries left %d, want 2: no resume handle falls back to one spent try", got)
	}
}

func TestAnExtendedWindowAndPausedTimeCarryAcrossResumes(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	now := t0
	r := openRig(t, Limits{SessionWall: time.Hour}, func(c *Collaborators) { c.Now = func() time.Time { return now } })
	sc := &sessCtx{started: t0, clk: &Clocks{Started: t0, Extended: 30 * time.Minute, PausedFor: 10 * time.Minute}}

	now = t0.Add(85 * time.Minute)
	if r.m.windowEnded(sc) {
		t.Error("the window ended at 85 minutes: base 60 + extra 30 + paused 10 is 100")
	}
	now = t0.Add(100 * time.Minute)
	if !r.m.windowEnded(sc) {
		t.Error("the window did not end at 100 minutes")
	}
}

func TestReconcileKeepsTheFailingOutputForTheNextBrief(t *testing.T) {
	r, _ := capRig(t)
	setCheck(r, "boom-done-time", "1")
	left, err := r.m.reconcile(context.Background())
	if err != nil || len(left) != 1 {
		t.Fatalf("reconcile: %v, %v", left, err)
	}
	if got := strings.Join(r.m.takeLastCheck(), "\n"); !strings.Contains(got, "boom-done-time") {
		t.Fatalf("kept output = %q", got)
	}
}

func TestADoneResultThatCannotResumeRunsTheOpenChecksOnce(t *testing.T) {
	r := openRig(t, Limits{}, nil)
	log := filepath.Join(t.TempDir(), "runs")
	r.m.c.Commands["fail"] = []string{"sh", "-c", "echo x >> " + log + "; exit 1"}
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		func(s llm.Session) (llm.Stream, error) {
			r.m.c.Commands["fail"] = []string{"true"}
			return building(r.wt, "a.go", "package a\n", builderDone)(s)
		})
	r.reachCleanup()
	b, _ := os.ReadFile(log)
	if runs := strings.Count(string(b), "x"); runs != 1 {
		t.Errorf("the open check ran %d times for one done result, want 1", runs)
	}
}
