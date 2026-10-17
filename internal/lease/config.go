package lease

import "time"

// LockTimeout turns the value of lease.lock_timeout_s into a duration; a missing, non-numeric or
// non-positive value gives the default.
// The caller reads the key from the Configuration Manager and passes its value.
func LockTimeout(seconds any) time.Duration {
	var secs float64

	switch v := seconds.(type) {
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
