package engine

import (
	"context"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// The real Task Manager serves over task.Channels, which the engine holds from the other end.
var _ func(*task.Manager, context.Context, task.Channels) error = (*task.Manager).Serve

// realTasks starts the real Task Manager behind the engine's TaskGroups interface.
type realTasks struct{}

var _ TaskGroups = realTasks{}

// Start builds a real task.Manager, serves it on a goroutine and returns the engine's end of its channels.
func (realTasks) Start(ctx context.Context, spec GroupSpec, h cache.Handle) (Channels, error) {
	tasks := make([]task.TaskSpec, 0, len(spec.Tasks))
	for _, id := range spec.Tasks {
		tasks = append(tasks, task.TaskSpec{ID: id})
	}
	m, err := task.New(task.GroupSpec{
		Run: spec.Run, Group: spec.Label, Tasks: tasks, Files: spec.Files, OtherFiles: spec.OtherFiles,
		Lease: task.LeaseRef{Branch: spec.Lease.Branch, Worktree: spec.Lease.Worktree, Fence: int64(spec.Lease.Fence)},
	}, task.Collaborators{Cache: h})
	if err != nil {
		return Channels{}, err
	}
	cmds := make(chan task.Command, CommandBuffer)
	replies := make(chan task.Message, CommandBuffer)
	go func() { _ = m.Serve(ctx, task.Channels{Commands: cmds, Replies: replies}) }()
	return Channels{Commands: cmds, Replies: replies}, nil
}

func TestRealTaskManagerEndsAndClosesItsReplyChannelOnAStopCommand(t *testing.T) {
	spec := GroupSpec{ID: "g1", Label: "tg-1", Run: "run-1", Tasks: []string{"t1"}}
	spec.Lease.Worktree = t.TempDir()
	ch, err := realTasks{}.Start(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch.Commands <- stopCommand("stop")
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch.Replies:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("the real Task Manager did not end on a stop command")
		}
	}
}
