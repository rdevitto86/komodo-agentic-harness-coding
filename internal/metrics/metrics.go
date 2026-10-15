package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// retentionInterval is how often the loop trims the folders it knows after the trim at a folder's first run-start.
const retentionInterval = 24 * time.Hour

// reposFile lists, in the machine metrics folder, every repository metrics folder the Service has opened.
const reposFile = "repos.json"

// Options holds the values the Configuration Manager hands to New.
type Options struct {
	RetentionDays int
}

// repoState is one repository's metrics folder and its figures.
type repoState struct {
	dir string
	fig *figures
}

// failLine is the line engine.jsonl holds for a failure that belongs to no run's figures.
type failLine struct {
	Failure bool      `json:"failure"`
	Time    time.Time `json:"time"`
	Run     bus.ID    `json:"run,omitempty"`
	Kind    string    `json:"kind"`
	Text    string    `json:"text"`
}

// Service records every event and keeps the figures current.
type Service struct {
	bus  bus.Bus
	dir  string // the machine metrics folder, which holds engine.jsonl and repos.json
	opt  Options
	mask func(string) string
	sub  *bus.Subscription

	mu     sync.Mutex            // guards repos, listed and every figures value; never held across file I/O
	repos  map[string]*repoState // loaded metrics folders
	listed map[string]bool       // every metrics folder repos.json names

	// The fields below belong to the goroutine that handles events.
	runRepo     map[bus.ID]string  // the metrics folder of each run that has not ended
	logs        map[string]*RunLog // open logs by path
	missed      map[string]int     // events lost to failed writes, by log, not yet marked
	failed      map[string]bool    // failures already recorded
	lastDropped uint64             // the subscription's drop count the last gap lines covered
}

// New returns a Service that writes under dir and masks secrets with mask.
// dir is the machine metrics folder; a run's logs go to the folder its run-start names.
func New(b bus.Bus, dir string, o Options, mask func(string) string) (*Service, error) {
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
	s := &Service{
		bus: b, dir: dir, opt: o, mask: mask, sub: sub,
		repos: map[string]*repoState{}, listed: map[string]bool{},
		runRepo: map[bus.ID]string{}, logs: map[string]*RunLog{}, missed: map[string]int{}, failed: map[string]bool{},
	}
	var dirs []string
	if err := readJSON(filepath.Join(dir, reposFile), &dirs); err == nil {
		for _, d := range dirs {
			s.listed[d] = true
		}
	}
	return s, nil
}

// Run subscribes to every event type and records until ctx ends, then records what is still queued.
func (s *Service) Run(ctx context.Context) error {
	defer s.closeLogs()
	// Handling must finish an event it began, so the context that ends the loop does not reach the work.
	work := context.WithoutCancel(ctx)
	tick := time.NewTicker(retentionInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.drain(work)
			return nil
		case <-tick.C:
			s.trimKnown(work)
		case e, ok := <-s.sub.Events:
			if !ok {
				s.flush()
				return nil
			}
			s.handle(work, e)
			if len(s.sub.Events) == 0 {
				s.flush()
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
				s.flush()
				return
			}
			s.handle(ctx, e)
		default:
			s.flush()
			return
		}
	}
}

// Rebuild recomputes groups.json and runs.json of every known repository from its run logs.
// It replays outside the lock, then swaps the new figures in, keeping the open-session map.
func (s *Service) Rebuild(ctx context.Context) error {
	s.mu.Lock()
	dirs := make([]string, 0, len(s.repos)+len(s.listed))
	for d := range s.listed {
		dirs = append(dirs, d)
	}
	for d := range s.repos {
		if d != "" && !s.listed[d] {
			dirs = append(dirs, d)
		}
	}
	s.mu.Unlock()
	var errs []error
	for _, d := range dirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		fig, err := rebuildFigures(ctx, d)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.mu.Lock()
		if r, ok := s.repos[d]; ok {
			fig.sessions = r.fig.sessions
			r.fig = fig
		} else {
			s.repos[d] = &repoState{dir: d, fig: fig}
		}
		fig.ver++
		s.mu.Unlock()
	}
	s.flush()
	return errors.Join(errs...)
}

// handle records one event: a gap line if events were dropped, the event copy, then the figures.
func (s *Service) handle(ctx context.Context, e bus.Event) {
	repo, path := s.target(ctx, e)
	s.markDrops()
	s.mu.Lock()
	rec := repo.fig.apply(e)
	ended := repo.fig.takeEnded()
	s.mu.Unlock()
	s.appendEvent(path, e.Run, e)
	if rec != nil && repo.dir != "" {
		if err := s.put(filepath.Join(repo.dir, "sessions.jsonl"), rec); err != nil {
			s.fail(e.Run, "write", err.Error())
		}
	}
	for _, run := range ended {
		s.forgetRun(run)
	}
}

// forgetRun closes a finished run's log and drops its entries from the Service's maps.
func (s *Service) forgetRun(run bus.ID) {
	if dir, ok := s.runRepo[run]; ok {
		p := filepath.Join(dir, LogName(run))
		s.closeLog(p)
		delete(s.missed, p)
	}
	delete(s.runRepo, run)
	prefix := string(run) + "\x00"
	for k := range s.failed {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(s.failed, k)
		}
	}
}

// target returns the repository state and log path an event belongs to; the path is empty when none is known.
func (s *Service) target(ctx context.Context, e bus.Event) (*repoState, string) {
	if e.Run == "" {
		return s.repoFor(""), filepath.Join(s.dir, machineLogName)
	}
	if !safeName(e.Run) {
		s.fail("", "run_id", fmt.Sprintf("run id %q cannot name a log file", e.Run))
		return s.repoFor(""), ""
	}
	if d, ok := e.Data.(*bus.RunStart); ok && e.Type == bus.TypeRunStart {
		s.openRepo(ctx, e.Run, d.CommonDir)
	}
	dir, ok := s.runRepo[e.Run]
	if !ok {
		var ended bool
		if dir, ended, ok = s.lookupRun(ctx, e.Run); ok && !ended {
			s.runRepo[e.Run] = dir
		}
	}
	if !ok {
		s.fail("", "no_repository", fmt.Sprintf("no repository known for run %s", e.Run))
		return s.repoFor(""), ""
	}
	return s.repoFor(dir), filepath.Join(dir, LogName(e.Run))
}

// repoFor returns the state of a metrics folder, creating it empty; the empty folder holds figures that are never written.
func (s *Service) repoFor(dir string) *repoState {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[dir]
	if !ok {
		r = &repoState{dir: dir, fig: newFigures()}
		r.fig.dir = dir
		s.repos[dir] = r
	}
	return r
}

// openRepo binds a run to the metrics folder inside its git common directory, loading and trimming the folder on first use.
func (s *Service) openRepo(ctx context.Context, run bus.ID, commonDir string) {
	if commonDir == "" {
		s.fail("", "no_common_dir", fmt.Sprintf("the run-start of run %s names no git directory", run))
		return
	}
	dir := RepoDir(filepath.Clean(commonDir))
	s.mu.Lock()
	_, loaded := s.repos[dir]
	s.mu.Unlock()
	if !loaded {
		s.loadRepo(ctx, dir, true)
	}
	s.runRepo[run] = dir
}

// loadRepo reads a folder's figures into memory; with first set it also trims the folder and lists it in repos.json.
func (s *Service) loadRepo(ctx context.Context, dir string, first bool) {
	var deleted []bus.ID
	if first {
		deleted = s.trimRepo(ctx, dir)
	}
	groups, runs, err := readFigures(ctx, dir)
	if err != nil {
		s.fail("", "read", err.Error())
	}
	s.mu.Lock()
	r, ok := s.repos[dir]
	if !ok {
		r = &repoState{dir: dir, fig: newFigures()}
		r.fig.dir = dir
		s.repos[dir] = r
	}
	r.fig.load(groups, runs)
	r.fig.dropRuns(deleted)
	known := s.listed[dir]
	s.listed[dir] = true
	list := slices.Sorted(mapKeys(s.listed))
	s.mu.Unlock()
	if first && !known {
		b, _ := json.Marshal(list)
		err := os.MkdirAll(s.dir, dirPerm)
		if err == nil {
			err = core.WriteFileAtomic(filepath.Join(s.dir, reposFile), b, filePerm)
		}
		if err != nil {
			s.fail("", "write", err.Error())
		}
	}
}

// mapKeys yields the keys of a set.
func mapKeys(m map[string]bool) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// lookupRun finds the metrics folder of a run from the runs.json files in memory, then from those of listed folders not yet loaded.
func (s *Service) lookupRun(ctx context.Context, run bus.ID) (dir string, ended, ok bool) {
	find := func() (todo []string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, r := range s.repos {
			if rm, found := r.fig.runs[run]; found {
				dir, ended, ok = r.dir, !rm.EndedAt.IsZero(), true
				return nil
			}
		}
		for d := range s.listed {
			if _, loaded := s.repos[d]; !loaded {
				todo = append(todo, d)
			}
		}
		return todo
	}
	todo := find()
	if ok || len(todo) == 0 {
		return dir, ended, ok
	}
	for _, d := range todo {
		s.loadRepo(ctx, d, false)
	}
	find()
	return dir, ended, ok
}

// trimRepo trims a folder and returns the runs whose logs it deleted; its failures go to the Service's own records.
func (s *Service) trimRepo(ctx context.Context, dir string) []bus.ID {
	res, err := trimDir(ctx, dir, s.opt.RetentionDays, now(), s.openRuns(dir))
	if err != nil {
		s.fail("", "trim", fmt.Sprintf("trim %s: %v", dir, err))
	}
	if res.OverBound {
		s.fail("", "size_bound", fmt.Sprintf("the logs of runs that have not ended in %s are over the size bound", dir))
	}
	return res.Deleted
}

// openRuns returns the runs of a folder that have not ended.
func (s *Service) openRuns(dir string) map[bus.ID]bool {
	out := map[bus.ID]bool{}
	for run, d := range s.runRepo {
		if d == dir {
			out[run] = true
		}
	}
	return out
}

// trimKnown trims every loaded folder and drops the figures of the runs whose logs went.
func (s *Service) trimKnown(ctx context.Context) {
	s.mu.Lock()
	var dirs []string
	for d := range s.repos {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	s.mu.Unlock()
	for _, d := range dirs {
		deleted := s.trimRepo(ctx, d)
		s.mu.Lock()
		s.repos[d].fig.dropRuns(deleted)
		s.mu.Unlock()
	}
}

// markDrops writes a gap line to the log of every open run, or to engine.jsonl with none open, when the bus dropped events.
func (s *Service) markDrops() {
	d := s.sub.Dropped()
	if d <= s.lastDropped {
		return
	}
	gap := gapLine{Gap: true, Time: now().UTC().Truncate(time.Second), Dropped: int(d - s.lastDropped)}
	type target struct {
		path string
		run  bus.ID
	}
	var targets []target
	for run, dir := range s.runRepo {
		targets = append(targets, target{filepath.Join(dir, LogName(run)), run})
	}
	if len(targets) == 0 {
		targets = append(targets, target{filepath.Join(s.dir, machineLogName), ""})
	}
	slices.SortFunc(targets, func(a, b target) int { return compareStrings(a.path, b.path) })
	ok := true
	for _, t := range targets {
		if err := s.put(t.path, gap); err != nil {
			ok = false
			s.fail(t.run, "write", err.Error())
		}
	}
	if ok {
		s.lastDropped = d
	}
}

// compareStrings orders two strings.
func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// appendEvent appends an event copy to the log at path; a failed write is recorded and later marked as a gap in that log.
func (s *Service) appendEvent(path string, run bus.ID, e bus.Event) {
	if path == "" {
		return
	}
	if n := s.missed[path]; n > 0 {
		if err := s.put(path, gapLine{Gap: true, Time: now().UTC().Truncate(time.Second), Dropped: n}); err == nil {
			delete(s.missed, path)
		}
	}
	if err := s.put(path, e); err != nil {
		s.missed[path]++
		s.fail(run, "write", err.Error())
	}
}

// put masks v's JSON line and appends it to the log at path.
func (s *Service) put(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode line for %s: %w", path, err)
	}
	l, ok := s.logs[path]
	if !ok {
		if l, err = openLog(path); err != nil {
			return err
		}
		s.logs[path] = l
	}
	if err := l.Append([]byte(s.mask(string(b)))); err != nil {
		s.closeLog(path)
		return err
	}
	return nil
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
	for p := range s.logs {
		s.closeLog(p)
	}
}

// fail records a failure once: in the run's figures when a run applies and its folder is known, otherwise in engine.jsonl.
func (s *Service) fail(run bus.ID, kind, text string) {
	key := string(run) + "\x00" + kind + "\x00" + text
	if s.failed[key] {
		return
	}
	s.failed[key] = true
	at := now().UTC().Truncate(time.Second)
	if run != "" {
		s.mu.Lock()
		if dir, ok := s.runRepo[run]; ok {
			if r, ok := s.repos[dir]; ok && r.fig.runs[run] != nil {
				r.fig.runs[run].Failures = append(r.fig.runs[run].Failures, FaultRecord{Time: at, Kind: kind, Text: text})
				r.fig.ver++
				s.mu.Unlock()
				return
			}
		}
		s.mu.Unlock()
	}
	// A failure to write this line has nowhere further to go.
	_ = s.put(filepath.Join(s.dir, machineLogName), failLine{Failure: true, Time: at, Run: run, Kind: kind, Text: text})
}

// flush writes groups.json and runs.json of every repository whose figures changed; a failure leaves them marked for the next batch.
func (s *Service) flush() {
	type job struct {
		r      *repoState
		groups map[bus.ID]map[bus.ID]GroupMetrics
		runs   map[bus.ID]RunMetrics
		ver    uint64
	}
	var jobs []job
	s.mu.Lock()
	for _, r := range s.repos {
		if r.dir != "" && r.fig.dirty() {
			g, rs := r.fig.snapshot()
			jobs = append(jobs, job{r, g, rs, r.fig.ver})
		}
	}
	s.mu.Unlock()
	for _, j := range jobs {
		err := s.writeFigure(filepath.Join(j.r.dir, "groups.json"), j.groups)
		if err == nil {
			err = s.writeFigure(filepath.Join(j.r.dir, "runs.json"), j.runs)
		}
		if err != nil {
			s.fail("", "write", err.Error())
			continue
		}
		s.mu.Lock()
		if j.r.fig.ver == j.ver {
			j.r.fig.written = j.ver
		}
		s.mu.Unlock()
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
