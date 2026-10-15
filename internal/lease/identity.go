package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// machineIDForm is the shape of an id: 32 to 36 hexadecimal characters and dashes.
var machineIDForm = regexp.MustCompile(`^[0-9a-f-]{32,36}$`)

// homeIDForm is the shape of an id drawn into the home folder: 32 lowercase hexadecimal characters.
var homeIDForm = regexp.MustCompile(`^[0-9a-f]{32}$`)

// linuxIDFiles lists where Linux keeps the machine id, in the order they are tried.
var linuxIDFiles = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}

// osMachineID returns the id the operating system gives this machine, or "" when it gives none; tests replace it.
var osMachineID = func() string {
	switch runtime.GOOS {
	case "linux":
		return firstFileID(linuxIDFiles)
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			return ""
		}
		return parseIOPlatformUUID(out)
	}
	return ""
}

// firstFileID returns the first well-formed id found in the files, lowercased, or "" when there is none.
func firstFileID(paths []string) string {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			if id := strings.ToLower(strings.TrimSpace(string(b))); machineIDForm.MatchString(id) {
				return id
			}
		}
	}
	return ""
}

// ioUUIDLine finds the platform UUID in the output of ioreg.
var ioUUIDLine = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f-]{32,36})"`)

// parseIOPlatformUUID returns the lowercased IOPlatformUUID in ioreg output, or "" when it is absent.
func parseIOPlatformUUID(out []byte) string {
	if m := ioUUIDLine.FindSubmatch(out); m != nil {
		return strings.ToLower(string(m[1]))
	}
	return ""
}

// machineID returns the id the operating system gives this machine; only without one does it use <home>/state/machine-id,
// drawing a random value when that file is absent or malformed. A home folder shared by several machines is unsupported.
func machineID(home string) (string, error) {
	if id := osMachineID(); id != "" {
		return id, nil
	}
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
	if id := strings.TrimSpace(string(b)); homeIDForm.MatchString(id) {
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
