package llm

import (
	"context"
	"testing"
	"time"
)

// drain reads events until an error and returns them with that error.
func drain(t *testing.T, st Stream) ([]StreamEvent, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var evs []StreamEvent
	for {
		ev, err := st.Next(ctx)
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
}
