package task

import (
	"context"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// serveRig runs Serve over a Manager whose saved stage decides what Run waits for.
type serveRig struct {
	m       *Manager
	cmds    chan Command
	replies chan Message
	done    chan error
}

// newServeRig starts Serve on a Manager with no collaborators beyond the worktree.
func newServeRig(t *testing.T, stage state.Stage) *serveRig {
	t.Helper()
	spec := GroupSpec{
		Run:   "run-1",
		Group: "tg-1",
		Lease: LeaseRef{Worktree: t.TempDir()},
		Saved: &Progress{Stage: stage},
	}
	m, err := New(spec, Collaborators{})
	if err != nil {
		t.Fatal(err)
	}
	r := &serveRig{m: m, cmds: make(chan Command, 4), replies: make(chan Message, 64), done: make(chan error, 1)}
	go func() { r.done <- m.Serve(context.Background(), Channels{Commands: r.cmds, Replies: r.replies}) }()
	return r
}

// next returns the next message of a kind, skipping others, or fails after two seconds.
func (r *serveRig) next(t *testing.T, kind MessageKind) Message {
	t.Helper()
	for {
		select {
		case msg, ok := <-r.replies:
			if !ok {
				t.Fatalf("the reply channel closed before a %s message", kind)
			}
			if msg.Kind == kind {
				return msg
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %s message", kind)
		}
	}
}

// ended waits for Serve to return and for its reply channel to close.
func (r *serveRig) ended(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		for range r.replies {
		}
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return")
		return nil
	}
}

func TestServeStopEndsTheLoopAndClosesTheReplies(t *testing.T) {
	r := newServeRig(t, state.StageCleanup)
	r.cmds <- Command{Kind: CmdStart}
	r.next(t, MsgState)
	r.cmds <- Command{Kind: CmdStop, Cause: "stop"}
	if msg := r.next(t, MsgEnded); msg.Ended.Result != "stopped" {
		t.Fatalf("ended %+v", msg.Ended)
	}
	if err := r.ended(t); err != nil {
		t.Fatal(err)
	}
}

func TestServeEndsWhenTheGroupIsClosed(t *testing.T) {
	r := newServeRig(t, state.StageCleanup)
	r.cmds <- Command{Kind: CmdStart}
	r.next(t, MsgState)
	if err := r.m.CloseTaskGroup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if msg := r.next(t, MsgEnded); msg.Ended.Result != "closed" {
		t.Fatalf("ended %+v", msg.Ended)
	}
	if err := r.ended(t); err != nil {
		t.Fatal(err)
	}
}

func TestServeReturnsATimerGrantToTheWaitingCall(t *testing.T) {
	r := newServeRig(t, state.StageCleanup)
	r.cmds <- Command{Kind: CmdStart}
	r.next(t, MsgState)
	got := make(chan TimerGrant, 1)
	go func() {
		g, _ := r.m.runtime().ExtendTimer(context.Background(), TimerRequest{
			Session: "s1",
			Used:    time.Minute,
			Limit:   time.Hour,
		})
		got <- g
	}()
	msg := r.next(t, MsgTimer)
	if msg.Timer == nil || msg.Timer.Session != "s1" {
		t.Fatalf("timer message %+v", msg)
	}
	r.cmds <- Command{Kind: CmdContinue, Re: msg.ID, Grant: &TimerGrant{Extra: 10 * time.Minute}}
	select {
	case g := <-got:
		if g.Extra != 10*time.Minute {
			t.Fatalf("grant %v", g)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExtendTimer did not return")
	}
	r.cmds <- Command{Kind: CmdStop, Cause: "stop"}
	_ = r.ended(t)
}

func TestServeAnswersAnEscalationOnceAndDropsTheSecondAnswer(t *testing.T) {
	r := newServeRig(t, state.StageBuild)
	r.cmds <- Command{Kind: CmdStart}
	first := r.next(t, MsgEscalate)
	if end := r.next(t, MsgEnded); end.Ended.Result != "held" {
		t.Fatalf("ended %+v", end.Ended)
	}
	ans := &Answer{Escalation: first.Escalation.ID, Action: "approve"}
	r.cmds <- Command{Kind: CmdContinue, Re: first.ID, Answer: ans}
	second := r.next(t, MsgEscalate)
	r.next(t, MsgEnded)
	if second.Escalation.ID == first.Escalation.ID {
		t.Fatal("the second escalation reused the first identifier")
	}
	r.cmds <- Command{Kind: CmdContinue, Re: first.ID, Answer: ans}
	r.cmds <- Command{Kind: CmdStop, Cause: "stop"}
	var dropped bool
	for msg := range r.replies {
		switch msg.Kind {
		case MsgEnded:
			if msg.Ended.Result != "stopped" {
				t.Fatalf("the repeated answer ran the group: %+v", msg.Ended)
			}
		case MsgState:
			for _, f := range msg.State.Facts {
				dropped = dropped || f.Subtype == "command.dropped"
			}
		}
	}
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Fatal("the dropped answer was not reported as a fact")
	}
}
