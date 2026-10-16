package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// lineNumber matches the :line and :line:column suffixes of a file reference.
var lineNumber = regexp.MustCompile(`:\d+(?::\d+)?`)

// Fingerprint hashes a sorted list of failing items into 64 lower-case hexadecimal characters; line numbers do not count.
func Fingerprint(items []string) string {
	norm := make([]string, len(items))
	for i, it := range items {
		norm[i] = strings.TrimSpace(lineNumber.ReplaceAllString(it, ""))
	}
	sort.Strings(norm)
	sum := sha256.Sum256([]byte(strings.Join(norm, "\n")))
	return hex.EncodeToString(sum[:])
}

// LoadLimits reads every bound once through the Configuration Manager.
func LoadLimits(cfg ConfigReader, agents AgentLimits) (Limits, error) {
	var l Limits
	ints := []struct {
		key string
		dst *int
	}{
		{"limits.returns", &l.Returns}, {"limits.group_attempts", &l.GroupAttempts}, {"limits.pause_max_min", &l.PauseMaxMin},
		{"limits.group_wall_min", &l.GroupWallMin}, {"limits.group_tokens", &l.GroupTokens},
		{"limits.pool_min", &l.PoolMin}, {"limits.pool_max", &l.PoolMax},
	}
	for _, k := range ints {
		n, err := readNumber(cfg, k.key)
		if err != nil {
			return Limits{}, err
		}
		*k.dst = int(n)
	}
	mem, err := readNumber(cfg, "sandbox.max_mem_gb")
	if err != nil {
		return Limits{}, err
	}
	l.SessionMemGB = mem
	for _, a := range []struct {
		name string
		dst  *int
	}{{"builder", &l.BuilderRetries}, {"reviewer", &l.ReviewerRetries}, {"tester", &l.TesterRetries}} {
		n, err := agents.Retries(a.name)
		if err != nil {
			return Limits{}, fmt.Errorf("%s retries: %w", a.name, err)
		}
		*a.dst = n
	}
	return l, nil
}

// readNumber returns the numeric value of one setting.
func readNumber(cfg ConfigReader, key string) (float64, error) {
	it, err := cfg.GetConfigItem(key)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", key, err)
	}
	switch v := it.Value.(type) {
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case float64:
		if v != math.Trunc(v) && key != "sandbox.max_mem_gb" {
			return 0, fmt.Errorf("read %s: %v is not a whole number", key, v)
		}
		return v, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", key, err)
		}
		return f, nil
	}
	return 0, fmt.Errorf("read %s: %v is not a number", key, it.Value)
}
