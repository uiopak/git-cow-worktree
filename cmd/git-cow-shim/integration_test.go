package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryRouting(t *testing.T) {
	git := realGit(t)
	cow := cowBinary(t)
	base := t.TempDir()
	repo := filepath.Join(base, "source repo 日本")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	proxyDir := filepath.Join(base, "proxy")
	if err := os.Mkdir(proxyDir, 0755); err != nil {
		t.Fatal(err)
	}
	proxy := filepath.Join(proxyDir, executableName("git"))
	copyExecutable(t, shimBinary, proxy)
	env := withEnv(cleanEnv(t), realGitEnv, git)
	env = withEnv(env, "PATH", strings.Join([]string{proxyDir, filepath.Dir(cow), filepath.Dir(git), envValue(env, "PATH")}, string(os.PathListSeparator)))
	env = withEnv(env, "GIT_COW_SHIM_TRACE", "1")
	runGit := func(dir string, args ...string) string {
		t.Helper()
		out, stderr, code := capture(t, git, args, env, dir, "")
		if code != 0 {
			t.Fatalf("git %q: exit=%d\n%s%s", args, code, out, stderr)
		}
		return strings.TrimSpace(out)
	}
	runProxy := func(args ...string) (string, string, int) {
		t.Helper()
		return capture(t, proxy, args, env, base, "")
	}
	runGit(repo, "init", "--initial-branch=main")
	runGit(repo, "config", "user.name", "Shim Test")
	runGit(repo, "config", "user.email", "shim-test@example.invalid")
	runGit(repo, "config", "core.autocrlf", "false")
	content := []byte("clean source Ω\n")
	if err := os.WriteFile(filepath.Join(repo, "tracked 日本.txt"), content, 0644); err != nil {
		t.Fatal(err)
	}
	large := bytes.Repeat([]byte("0123456789abcdef"), 65536)
	if err := os.WriteFile(filepath.Join(repo, "binary.dat"), large, 0644); err != nil {
		t.Fatal(err)
	}
	runGit(repo, "add", ".")
	runGit(repo, "commit", "-m", "fixture")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("must not be copied"), 0644); err != nil {
		t.Fatal(err)
	}
	common := runGit(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	checkWorktree := func(path, branch string) {
		t.Helper()
		if status := runGit(path, "status", "--porcelain"); status != "" {
			t.Fatalf("worktree is dirty: %s", status)
		}
		if got := runGit(path, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != common {
			t.Fatalf("common dir %q != %q", got, common)
		}
		for name, want := range map[string][]byte{"tracked 日本.txt": content, "binary.dat": large} {
			got, err := os.ReadFile(filepath.Join(path, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s content mismatch: %v", name, err)
			}
		}
		if _, err := os.Stat(filepath.Join(path, "untracked.txt")); !os.IsNotExist(err) {
			t.Fatalf("copied untracked source: %v", err)
		}
		if branch != "" && runGit(path, "symbolic-ref", "--short", "HEAD") != branch {
			t.Fatalf("wrong branch in %s", path)
		}
	}

	target := filepath.Join(base, "T3 target Ω")
	out, stderr, code := runProxy("-C", base, "-C", filepath.Base(repo), "-c", "checkout.workers=2", "worktree", "add", "-b", "t3-topic", target, "HEAD")
	if code != 0 || !strings.Contains(stderr, "route=cow ") {
		t.Fatalf("T3 add: %d\n%s%s", code, out, stderr)
	}
	checkWorktree(target, "t3-topic")
	// Modifying one worktree must not change the source, on both COW-capable
	// filesystems and the native NTFS copy fallback.
	if err := os.WriteFile(filepath.Join(target, "binary.dat"), []byte("target only"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "binary.dat")); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("source changed after modifying target: %v", err)
	}
	runGit(target, "restore", "binary.dat")

	detached := filepath.Join(base, "detached path 日本")
	out, stderr, code = runProxy("-C", repo, "worktree", "add", "--detach", detached, "HEAD")
	if code != 0 || !strings.Contains(stderr, "route=cow ") {
		t.Fatalf("detached add: %d\n%s%s", code, out, stderr)
	}
	checkWorktree(detached, "")
	if _, _, code := capture(t, git, []string{"symbolic-ref", "HEAD"}, env, detached, ""); code == 0 {
		t.Fatal("expected detached HEAD")
	}

	// After --, flag-like paths are positional and cannot disable interception.
	flagPath := filepath.Join(repo, "--no-checkout")
	out, stderr, code = runProxy("-C", repo, "worktree", "add", "--detach", "--", "--no-checkout", "HEAD")
	if code != 0 || !strings.Contains(stderr, "route=cow ") {
		t.Fatalf("separator add: %d\n%s%s", code, out, stderr)
	}
	checkWorktree(flagPath, "")

	noCheckout := filepath.Join(base, "without checkout")
	out, stderr, code = runProxy("-C", repo, "worktree", "add", "--no-checkout", "--detach", noCheckout, "HEAD")
	if code != 0 || !strings.Contains(stderr, "route=no checkout") {
		t.Fatalf("no-checkout pass-through: %d\n%s%s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(noCheckout, "binary.dat")); !os.IsNotExist(err) {
		t.Fatalf("no-checkout unexpectedly populated target: %v", err)
	}

	for _, args := range [][]string{
		{"-C", repo, "worktree", "add", "--detach", target, "HEAD"},
		{"-C", repo, "worktree", "add", "-b", "t3-topic", filepath.Join(base, "duplicate branch"), "HEAD"},
	} {
		out, stderr, code = runProxy(args...)
		if code == 0 || !strings.Contains(stderr, "route=cow ") {
			t.Fatalf("expected COW add error: %d\n%s%s", code, out, stderr)
		}
	}
	for _, path := range []string{target, detached, flagPath, noCheckout} {
		out, stderr, code = runProxy("-C", repo, "worktree", "remove", "--force", path)
		if code != 0 || !strings.Contains(stderr, "route=ordinary command") {
			t.Fatalf("ordinary removal: %d\n%s%s", code, out, stderr)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("worktree remains after removal: %v", err)
		}
	}

	unknownTarget := filepath.Join(base, "unknown must not exist")
	_, stderr, code = runProxy("-C", repo, "worktree", "add", "--git-cow-future-option", unknownTarget, "HEAD")
	if code == 0 || !strings.Contains(stderr, "route=unsupported add option") {
		t.Fatalf("unknown syntax was not passed through: %d %q", code, stderr)
	}
	if _, err := os.Stat(unknownTarget); !os.IsNotExist(err) {
		t.Fatalf("unknown syntax mutated repository: %v", err)
	}
}
