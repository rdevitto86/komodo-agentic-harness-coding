package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// Options holds the values the Configuration Manager hands to New.
type Options struct {
	RetentionDays int
}

// repoState is one repository's metrics folder and its figures.
type repoState struct {
	dir string
	fig *figures
}

// Service records every event and keeps the figures current.
type Service struct {
	bus  bus.Bus
	h    *fault.Handler
	dir  string // the machine metrics folder, which holds engine.jsonl
	opt  Options
	mask func(string) string
	sub  *bus.Subscription

	mu          sync.Mutex
	repos       map[string]*repoState // by metrics folder
	runRepo     map[bus.ID]string     // the metrics folder of each run
	logs        map[string]*RunLog    // open logs by path
	failing     map[string]bool       // targets whose failure was already reported
	lastDropped uint64                // the subscription's drop count at the last gap check
}

// New returns a Service that writes to dir, reports its own failures to h and masks secrets with mask.
// dir is the machine metrics folder; a run's logs go to its repository's folder, found from its run-start.
func New(b bus.Bus, h *fault.Handler, dir string, o Options, mask func(string) string) (*Service, error) {
	if b == nil {
		return nil, errors.New("metrics needs an event bus")
	}
	if mask == nil {
		mask = func(s string) string { return s }
	}
	sub, err := b.Subscribe("metrics")
	if err != nil {
		return nil, fmt.Errorf("subscribe to every event: %w", err)
	}
	return &Service{
		bus: b, h: h, dir: dir, opt: o, mask: mask, sub: sub,
		repos: map[string]*repoState{}, runRepo: map[bus.ID]string{}, logs: map[string]*RunLog{}, failing: map[string]bool{},
	}, nil
}

// Run subscribes to every event type and records until ctx ends, then records what is still queued.
func (s *Service) Run(ctx context.Context) error {
	defer s.closeLogs()
	// Handling must finish an event it began, so the context that ends the loop does not reach the work.
	work := context.WithoutCancel(ctx)
	for {
		select {
		case <-ctx.Done():
			s.drain(work)
			return nil
		case e, ok := <-s.sub.Events:
			if !ok {
				s.lockedFlush()
				return nil
			}
			s.handle(work, e)
			if len(s.sub.Events) == 0 {
				s.lockedFlush()
			}
		}
	}
}

// drain records every event already queued, then writes the figures.
func (s *Service) drain(ctx context.Context) {
	for {
		select {
		case e, ok := <-s.sub.Events:
			if !ok {
				s.lockedFlush()
				return
			}
			s.handle(ctx, e)
		default:
			s.lockedFlush()
			return
		}
	}
}

// Rebuild recomputes groups.json and runs.json from the run logs.
func (s *Service) Rebuild(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, r := range s.repos {
		if r.dir == "" {
			continue
		}
		fig, err := rebuildFigures(ctx, r.dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		fig.dirty = true
		r.fig = fig
	}
	s.flush()
	return errors.Join(errs...)
}

// lockedFlush writes the changed figures under the lock.
func (s *Service) lockedFlush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
}

// handle records one event: a gap line if events were dropped, the event copy, then the figures.
func (s *Service) handle(ctx context.Context, e bus.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, path := s.target(ctx, e)
	if d := s.sub.Dropped(); d > s.lastDropped {
		gap := gapLine{Gap: true, Time: now().UTC().Truncate(time.Second), Dropped: int(d - s.lastDropped)}
		s.lastDropped = d
		s.appendJSON(path, e.Run, gap)
	}
	s.appendJSON(path, e.Run, e)
	if rec := repo.fig.apply(e); rec != nil && repo.dir != "" {
		s.appendJSON(filepath.Join(repo.dir, "sessions.jsonl"), e.Run, rec)
	}
	if e.Type == bus.TypeRunComplete && path != "" {
		s.closeLog(path)
	}
}

// target returns the repository state and log path an event belongs to; the path is empty when none is known.
func (s *Service) target(ctx context.Context, e bus.Event) (*repoState, string) {
	if e.Run == "" {
		return s.repo(""), filepath.Join(s.dir, machineLogName)
	}
	if !safeName(e.Run) {
		s.reportOnce("run:"+string(e.Run), e.Run, fmt.Errorf("run id %q cannot name a log file", e.Run))
		return s.repo(""), ""
	}
	if d, ok := e.Data.(*bus.RunStart); ok && e.Type == bus.TypeRunStart {
		s.openRepo(ctx, e.Run, d.Root)
	}
	dir, ok := s.runRepo[e.Run]
	if !ok {
		s.reportOnce("run:"+string(e.Run), e.Run, fmt.Errorf("no repository known for run %s", e.Run))
		return s.repo(""), ""
	}
	return s.repo(dir), filepath.Join(dir, LogName(e.Run))
}

// repo returns the state of a metrics folder; the empty folder holds figures that are never written.
func (s *Service) repo(dir string) *repoState {
	r, ok := s.repos[dir]
	if !ok {
		r = &repoState{dir: dir, fig: newFigures()}
		s.repos[dir] = r
	}
	return r
}

// openRepo finds the metrics folder of the repository at root, trims it on first use and binds the run to it.
func (s *Service) openRepo(ctx context.Context, run bus.ID, root string) {
	common, err := commonDir(ctx, root)
	if err != nil {
		s.reportOnce("run:"+string(run), run, err)
		return
	}
	dir := RepoDir(common)
	if _, seen := s.repos[dir]; !seen {
		if err := trimDir(ctx, dir, s.opt.RetentionDays, now()); err != nil {
			s.reportOnce("trim:"+dir, run, fmt.Errorf("trim %s: %w", dir, err))
		}
		r := s.repo(dir)
		if groups, runs, err := readFigures(ctx, dir); err == nil {
			r.fig.load(groups, runs)
		}
	}
	s.runRepo[run] = dir
}

// appendJSON masks v's JSON line and appends it to the log at path; a failure is reported once per streak.
func (s *Service) appendJSON(path string, run bus.ID, v any) {
	if path == "" {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		s.reportOnce(path, run, fmt.Errorf("encode line for %s: %w", path, err))
		return
	}
	line := []byte(s.mask(string(b)))
	l, ok := s.logs[path]
	if !ok {
		if l, err = openLog(path); err != nil {
			s.reportOnce(path, run, err)
			return
		}
		s.logs[path] = l
	}
	if err := l.Append(line); err != nil {
		s.closeLog(path)
		s.reportOnce(path, run, err)
		return
	}
	delete(s.failing, path)
}

// closeLog closes the open log at path.
func (s *Service) closeLog(path string) {
	if l, ok := s.logs[path]; ok {
		_ = l.Close()
		delete(s.logs, path)
	}
}

// closeLogs closes every open log.
func (s *Service) closeLogs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.logs {
		s.closeLog(p)
	}
}

// reportOnce sends err to the Error Handler unless the same target already failed and has not recovered.
func (s *Service) reportOnce(key string, run bus.ID, err error) {
	if s.failing[key] {
		return
	}
	s.failing[key] = true
	if s.h != nil {
		s.h.Report(fault.Source{Component: "metrics", Run: run}, string(format.Internal), fault.KindInternal, err)
	}
}

// flush writes groups.json and runs.json of every repository whose figures changed; a failure leaves them marked for the next batch.
func (s *Service) flush() {
	for _, r := range s.repos {
		if r.dir == "" || !r.fig.dirty {
			continue
		}
		groups, runs := r.fig.snapshot()
		key := "figures:" + r.dir
		err := s.writeFigure(filepath.Join(r.dir, "groups.json"), groups)
		if err == nil {
			err = s.writeFigure(filepath.Join(r.dir, "runs.json"), runs)
		}
		if err != nil {
			s.reportOnce(key, "", err)
			continue
		}
		delete(s.failing, key)
		r.fig.dirty = false
	}
}

// writeFigure replaces a figures file with the masked JSON of v.
func (s *Service) writeFigure(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("create folder of %s: %w", path, err)
	}
	return core.WriteFileAtomic(path, []byte(s.mask(string(b))), filePerm)
}
