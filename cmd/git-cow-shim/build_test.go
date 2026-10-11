package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in because compiling every target is release validation, not a normal
// unit-test prerequisite. All artifacts belong to this test's temporary dir.
func TestCrossCompilation(t *testing.T) {
	if os.Getenv("GIT_COW_SHIM_CROSS_TEST") != "1" {
		t.Skip("set GIT_COW_SHIM_CROSS_TEST=1 for the cross-compilation matrix")
	}
	dir := t.TempDir()
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64", "windows/386"} {
		t.Run(target, func(t *testing.T) {
			platform, arch, _ := strings.Cut(target, "/")
			env := withEnv(os.Environ(), "GOOS", platform)
			env = withEnv(env, "GOARCH", arch)
			env = withEnv(env, "CGO_ENABLED", "0")
			name := strings.ReplaceAll(target, "/", "-")
			for _, args := range [][]string{
				{"build", "-o", filepath.Join(dir, name+"-shim"), "."},
				{"test", "-c", "-o", filepath.Join(dir, name+"-tests"), "."},
			} {
				cmd := exec.Command("go", args...)
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %q: %v\n%s", args, err, out)
				}
			}
		})
	}
}
