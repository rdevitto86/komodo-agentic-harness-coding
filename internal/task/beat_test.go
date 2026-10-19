package task

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// beatEvery is the beat period the tests run at.
const beatEvery = 10 * time.Millisecond

// beatCounter counts the beat messages on a reply channel.
type beatCounter struct{ n atomic.Int64 }

// drain reads the replies until the channel closes.
func (b *beatCounter) drain(replies <-chan Message) {
	for m := range replies {
		if m.Kind == MsgBeat {
			b.n.Add(1)
		}
	}
}

// waitBeats waits until at least n beats were counted.
func (b *beatCounter) waitBeats(t *testing.T, n int64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for b.n.Load() < n {
		select {
		case <-deadline:
			t.Fatalf("%d beats, want %d", b.n.Load(), n)
		case <-time.After(time.Millisecond):
		}
	}
}

// holdingStream returns a builder step whose session stays silent until release is closed, then returns a result.
func holdingStream(started chan<- struct{}, release <-chan struct{}) func(llm.Session) (llm.Stream, error) {
	return func(llm.Session) (llm.Stream, error) {
		st := newFakeStream()
		close(started)
		go func() {
			<-release
			st.ev <- resultEvent(`{"items":[]}`)
		}()
		return st, nil
	}
}

func TestServeBeatsWhileAModelSessionRuns(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	r := newPipeRig(t, rigOpts{
		limits: Limits{GroupRetries: -1},
		mut:    func(_ *GroupSpec, c *Collaborators) { c.BeatEvery = beatEvery },
	})
	r.model.on("builder", holdingStream(started, release))

	cmds, replies := make(chan Command, 4), make(chan Message, 64)
	served := make(chan error, 1)
	bc := &beatCounter{}

	go bc.drain(replies)
	go func() { served <- r.m.Serve(context.Background(), Channels{Commands: cmds, Replies: replies}) }()

	cmds <- Command{Kind: CmdStart}
	<-started

	base := bc.n.Load()
	bc.waitBeats(t, base+5)
	close(release)

	cmds <- Command{Kind: CmdStop, Cause: "stop"}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

// blockedRuntime is a Runtime whose state report waits until it is released.
type blockedRuntime struct {
	fakeRT
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

// ReportState signals that it was entered, then waits for the release.
func (b *blockedRuntime) ReportState(ctx context.Context, s StateReport) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return b.fakeRT.ReportState(ctx, s)
}

func TestServeStopsBeatingWhenItsLoopHangs(t *testing.T) {
	rt := &blockedRuntime{release: make(chan struct{}), entered: make(chan struct{})}
	r := newPipeRig(t, rigOpts{
		saved: &Progress{Stage: state.StageCleanup},
		mut: func(_ *GroupSpec, c *Collaborators) {
			c.BeatEvery = beatEvery
			c.Runtime = rt
		},
	})

	cmds, replies := make(chan Command, 4), make(chan Message, 1024)
	served := make(chan error, 1)
	bc := &beatCounter{}

	go bc.drain(replies)
	go func() { served <- r.m.Serve(context.Background(), Channels{Commands: cmds, Replies: replies}) }()

	bc.waitBeats(t, 3)

	// A pause on an idle group reports state inline, so the command loop blocks inside the report.
	cmds <- Command{Kind: CmdPause, Link: "claude-cli"}
	<-rt.entered
	time.Sleep(3 * beatEvery)

	settled := bc.n.Load()
	time.Sleep(20 * beatEvery)

	if got := bc.n.Load(); got != settled {
		t.Fatalf("a hung loop kept beating: %d beats, then %d", settled, got)
	}

	close(rt.release)
	cmds <- Command{Kind: CmdStop, Cause: "stop"}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
