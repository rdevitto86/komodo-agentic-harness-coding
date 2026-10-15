package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// machineIDForm is the shape of a machine id: 32 lowercase hexadecimal characters.
var machineIDForm = regexp.MustCompile(`^[0-9a-f]{32}$`)

// machineID returns the id kept in <home>/state/machine-id, drawing a random one when the file is absent or malformed.
// A holder on the same machine always carries the same id, whatever the network calls the machine.
func machineID(home string) (string, error) {
	dir := filepath.Join(home, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create state folder: %w", err)
	}
	unlock, err := core.FileLock(context.Background(), filepath.Join(dir, "machine-id.lock"), 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("lock machine id: %w", err)
	}
	defer func() { _ = unlock() }()
	path := filepath.Join(dir, "machine-id")
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read machine id: %w", err)
	}
	if id := strings.TrimSpace(string(b)); machineIDForm.MatchString(id) {
		return id, nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("draw machine id: %w", err)
	}
	id := hex.EncodeToString(raw[:])
	if err := core.WriteFileAtomic(path, []byte(id+"\n"), 0o644); err != nil {
		return "", err
	}
	return id, nil
}
