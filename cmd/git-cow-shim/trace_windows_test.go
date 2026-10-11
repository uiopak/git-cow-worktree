//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestFileTraceLockedFileFailsOpen(t *testing.T) {
	log := filepath.Join(t.TempDir(), "locked trace.jsonl")
	const original = "existing data\n"
	if err := os.WriteFile(log, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(log)
	if err != nil {
		t.Fatal(err)
	}
	// Deny write sharing using a test-owned handle, independent of ACLs or
	// administrator privileges. The child must still complete successfully.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	env := withEnv(helperEnv(t, "cow-report"), traceFileEnv, log)
	out, stderr, code := capture(t, shimBinary, []string{"status"}, env, "", "")
	if code != 0 || !json.Valid([]byte(out)) || strings.Count(stderr, traceFileEnv) != 1 || !strings.HasSuffix(stderr, "helper stderr\n") {
		t.Fatalf("locked trace altered Git result: code=%d stderr=%q stdout=%q", code, stderr, out)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != original {
		t.Fatalf("locked trace changed: %v %q", err, data)
	}
}
