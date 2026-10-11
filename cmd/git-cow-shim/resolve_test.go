package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, dir, name, data string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, executableName(name))
	if err := os.WriteFile(path, []byte(data), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveRealGit(t *testing.T) {
	dir := t.TempDir()
	self := writeExecutable(t, filepath.Join(dir, "self"), "git", "shim")
	copy := writeExecutable(t, filepath.Join(dir, "copy"), "git", "shim")
	real := writeExecutable(t, filepath.Join(dir, "real"), "git", "real git")
	path := strings.Join([]string{filepath.Dir(self), filepath.Dir(copy), filepath.Dir(real)}, string(os.PathListSeparator))
	got, err := resolveRealGit(self, "", path)
	if err != nil || got != real {
		t.Fatalf("discovery = %q, %v", got, err)
	}
	got, err = resolveRealGit(self, real, "")
	if err != nil || got != real {
		t.Fatalf("explicit = %q, %v", got, err)
	}
	for _, explicit := range []string{self, copy, "git", filepath.Join(dir, "missing.exe"), dir} {
		if _, err := resolveRealGit(self, explicit, path); err == nil {
			t.Errorf("accepted invalid explicit executable %q", explicit)
		}
	}
	if _, err := resolveRealGit(self, "", filepath.Dir(self)); err == nil {
		t.Fatal("accepted PATH containing only self")
	}
	linkDir := filepath.Join(dir, "hardlink")
	if err := os.Mkdir(linkDir, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, executableName("git"))
	if err := os.Link(self, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	got, err = resolveRealGit(self, "", linkDir+string(os.PathListSeparator)+filepath.Dir(real))
	if err != nil || got != real {
		t.Fatalf("hard-link discovery = %q, %v", got, err)
	}
}

func TestResolveCowPrefersCompanion(t *testing.T) {
	dir := t.TempDir()
	self := writeExecutable(t, dir, "git-cow-shim", "shim")
	cow := writeExecutable(t, dir, "git-cow-worktree", "cow")
	other := writeExecutable(t, filepath.Join(dir, "other"), "git-cow-worktree", "other")
	if got := resolveCow(self, filepath.Dir(other)); got != cow {
		t.Fatalf("got %q, want %q", got, cow)
	}
	if err := os.Remove(cow); err != nil {
		t.Fatal(err)
	}
	if got := resolveCow(self, filepath.Dir(other)); got != other {
		t.Fatalf("PATH resolution = %q", got)
	}
	if got := resolveCow(self, ""); got != "" {
		t.Fatalf("missing companion resolution = %q", got)
	}
}
