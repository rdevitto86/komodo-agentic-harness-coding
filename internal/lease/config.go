package lease

import (
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
)

// ConfigReader is the part of the Configuration Manager the lease package reads.
type ConfigReader interface {
	GetConfigItem(key string) (config.Item, error)
}

// LockTimeout returns lease.lock_timeout_s as a duration; an unreadable or non-positive value gives the default.
func LockTimeout(cfg ConfigReader) time.Duration {
	if cfg == nil {
		return DefaultLockTimeout
	}
	it, err := cfg.GetConfigItem("lease.lock_timeout_s")
	if err != nil {
		return DefaultLockTimeout
	}
	var secs float64
	switch v := it.Value.(type) {
	case int:
		secs = float64(v)
	case int64:
		secs = float64(v)
	case float64:
		secs = v
	}
	if secs <= 0 {
		return DefaultLockTimeout
	}
	return time.Duration(secs * float64(time.Second))
}
