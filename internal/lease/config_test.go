package lease

import (
	"errors"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
)

// fakeConfig serves one value or one error for any key.
type fakeConfig struct {
	v   any
	err error
}

// GetConfigItem returns the canned item.
func (f fakeConfig) GetConfigItem(key string) (config.Item, error) {
	return config.Item{Key: key, Value: f.v}, f.err
}

func TestLockTimeout(t *testing.T) {
	cases := []struct {
		name string
		cfg  fakeConfig
		want time.Duration
	}{
		{"int", fakeConfig{v: 5}, 5 * time.Second},
		{"float from json", fakeConfig{v: float64(7)}, 7 * time.Second},
		{"int64", fakeConfig{v: int64(9)}, 9 * time.Second},
		{"zero falls back", fakeConfig{v: 0}, DefaultLockTimeout},
		{"wrong type falls back", fakeConfig{v: "x"}, DefaultLockTimeout},
		{"read error falls back", fakeConfig{err: errors.New("boom")}, DefaultLockTimeout},
	}
	for _, c := range cases {
		if got := LockTimeout(c.cfg); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if got := LockTimeout(nil); got != DefaultLockTimeout {
		t.Errorf("nil reader: got %v", got)
	}
}

func TestLockTimeoutFromRealConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KOMODO_HOME", home)
	cm, err := config.New(home, config.Known{}, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if got := LockTimeout(cm); got != 30*time.Second {
		t.Fatalf("default: %v", got)
	}
	if _, err := cm.SetConfigItem(config.Change{Key: "lease.lock_timeout_s", New: 12}); err != nil {
		t.Fatal(err)
	}
	if got := LockTimeout(cm); got != 12*time.Second {
		t.Fatalf("after set: %v", got)
	}
}
