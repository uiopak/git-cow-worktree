package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E_FailedHookRunsOnce(t *testing.T) {
	bin := buildGitCowWorktree(t)
	r := newRepo(t, "failed-hook")
	r.commit("large.txt", strings.Repeat("shared contents\n", 8192))
	log := filepath.Join(r.parent, "hook.log")
	hook := "#!/bin/sh\nprintf 'called\\n' >> " + shellQuote(log) + "\nprintf 'hook stdout\\n'\nexit 73\n"
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.parent, "result")
	cmd := exec.Command(bin, "add", "-v", "-b", "failed-hook", path, "HEAD")
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 73 {
		t.Fatalf("hook exit = %v, want 73\n%s", err, out)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "called\n" {
		t.Fatalf("hook ran more than once: %q, %v\n%s", data, err, out)
	}
	if strings.Contains(string(out), "retrying from scratch") {
		t.Fatalf("hook failure retried checkout:\n%s", out)
	}
	if got := gitOutput(t, path, "status", "--porcelain"); got != "" {
		t.Fatalf("completed worktree dirty after hook failure: %s", got)
	}
	if cowSupported(t, r.parent) && !strings.Contains(string(out), "verified 1 files") {
		t.Fatalf("test did not exercise a seeded index:\n%s", out)
	}
}

func TestE2E_CheckoutFailureRollback(t *testing.T) {
	bin := buildGitCowWorktree(t)
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-path-new-branch", true: "existing-empty-path-existing-branch"}[existing], func(t *testing.T) {
			r := newRepo(t, "rollback")
			r.commit("safe.txt", strings.Repeat("eligible\n", 4096))
			r.commit("bad.txt", "smudge fails\n")
			r.commit(".gitattributes", "bad.txt filter=broken\n")
			r.run("git", "config", "filter.broken.clean", "cat")
			r.run("git", "config", "filter.broken.smudge", "exit 19")
			r.run("git", "config", "filter.broken.required", "true")
			path := filepath.Join(r.parent, "failed")
			flag := "-b"
			if existing {
				r.branch("rollback-test", "HEAD")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				flag = "-B"
			}
			cmd := exec.Command(bin, "add", flag, "rollback-test", "--lock", path, "HEAD")
			cmd.Dir = r.dir
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("checkout unexpectedly succeeded: %s", out)
			}
			wts, err := listWorktrees(r.dir)
			if err != nil || len(wts) != 1 {
				t.Fatalf("failed worktree still registered: %v, %v\n%s", wts, err, out)
			}
			_, statErr := os.Stat(path)
			if existing {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("existing empty directory not restored: %v, %v", entries, err)
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("new failed path still exists: %v\n%s", statErr, out)
			}
			branches, err := branchHeads(r.dir)
			if err != nil {
				t.Fatal(err)
			}
			_, remains := branches["refs/heads/rollback-test"]
			if remains != existing {
				t.Fatalf("branch remains=%v, want %v", remains, existing)
			}
		})
	}
}

func TestE2E_ClaudeHookLifecycle(t *testing.T) {
	bin := buildGitCowWorktree(t)
	r := newRepo(t, "claude-space-żółć")
	r.commit("data.txt", strings.Repeat("cow contents\n", 8192))
	r.commit(".gitignore", ".claude/worktrees/\n.env\nconfig/*.local\n")
	if err := os.WriteFile(filepath.Join(r.dir, ".worktreeinclude"), []byte(".env\nconfig/*.local\npublic.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, ".env"), []byte("LOCAL_SECRET=example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "public.txt"), []byte("untracked and not ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(r.dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "config", "dev.local"), []byte("local config"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutHook := "#!/bin/sh\nprintf 'hook writes to stdout\\n'\n"
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "hooks", "post-checkout"), []byte(stdoutHook), 0o755); err != nil {
		t.Fatal(err)
	}
	in := claudeHookInput{Cwd: r.dir, Name: "feature-space", Event: "WorktreeCreate"}
	out, diagnostics, err := invokeClaudeHook(t, bin, in, "create", "--base-ref", "HEAD", "--copy-local")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, diagnostics)
	}
	path := strings.TrimSpace(out)
	want, _ := absClean(filepath.Join(r.dir, ".claude", "worktrees", in.Name))
	if path != want || strings.Count(out, "\n") != 1 {
		t.Fatalf("hook stdout=%q, want one absolute path %q", out, want)
	}
	if !strings.Contains(diagnostics, "hook writes to stdout") {
		t.Fatalf("hook diagnostics were lost: %s", diagnostics)
	}
	if got := gitOutput(t, path, "symbolic-ref", "HEAD"); got != "refs/heads/worktree-feature-space" {
		t.Fatalf("branch = %s", got)
	}
	if got := gitOutput(t, path, "status", "--porcelain"); got != "" {
		t.Fatalf("new worktree dirty: %s", got)
	}
	for _, file := range []string{".env", "config/dev.local"} {
		data, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(file)))
		original, _ := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(file)))
		if err != nil || !bytes.Equal(data, original) {
			t.Fatalf("local file %s not copied: %v", file, err)
		}
	}
	if _, err := os.Stat(filepath.Join(path, "public.txt")); !os.IsNotExist(err) {
		t.Fatal("copied an untracked file that is not ignored")
	}
	if err := os.WriteFile(filepath.Join(path, "data.txt"), []byte("local edits"), 0o644); err != nil {
		t.Fatal(err)
	}
	if original, _ := os.ReadFile(filepath.Join(r.dir, "data.txt")); string(original) == "local edits" {
		t.Fatal("worktree modification changed source")
	}
	if reused, diag, err := invokeClaudeHook(t, bin, in, "create"); err != nil || reused != out {
		t.Fatalf("reuse failed: %v %q\n%s", err, reused, diag)
	}
	remove := claudeHookInput{Cwd: r.dir, WorktreePath: path, Event: "WorktreeRemove"}
	if _, _, err := invokeClaudeHook(t, bin, remove, "remove"); err == nil {
		t.Fatal("dirty worktree removed without --force")
	}
	if _, diag, err := invokeClaudeHook(t, bin, remove, "remove", "--force"); err != nil {
		t.Fatalf("remove: %v\n%s", err, diag)
	}
	branches, _ := branchHeads(r.dir)
	if _, remains := branches["refs/heads/worktree-feature-space"]; remains {
		t.Fatal("unchanged owned branch was not removed")
	}
	if _, diag, err := invokeClaudeHook(t, bin, remove, "remove"); err != nil {
		t.Fatalf("idempotent remove: %v\n%s", err, diag)
	}
}

func TestE2E_ClaudeHookKeepsCommitsAndRejectsUnowned(t *testing.T) {
	bin := buildGitCowWorktree(t)
	r := newRepo(t, "claude-ownership")
	r.commit("a.txt", "base\n")
	r.commit(".gitignore", ".claude/worktrees/\n")
	in := claudeHookInput{Cwd: r.dir, Name: "owned"}
	out, diag, err := invokeClaudeHook(t, bin, in, "create")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, diag)
	}
	path := strings.TrimSpace(out)
	gitOutput(t, path, "commit", "--allow-empty", "-m", "keep this commit")
	tip := gitOutput(t, path, "rev-parse", "HEAD")
	if _, diag, err := invokeClaudeHook(t, bin, claudeHookInput{Cwd: r.dir, WorktreePath: path}, "remove"); err != nil {
		t.Fatalf("remove: %v\n%s", err, diag)
	}
	if got := gitOutput(t, r.dir, "rev-parse", "worktree-owned"); got != tip {
		t.Fatal("cleanup lost an agent commit")
	}
	manual := filepath.Join(r.parent, "manual")
	r.run("git", "worktree", "add", "--detach", manual, "HEAD")
	if _, _, err := invokeClaudeHook(t, bin, claudeHookInput{Cwd: r.dir, WorktreePath: manual}, "remove", "--force"); err == nil {
		t.Fatal("removed an unowned Git worktree")
	}
	if _, err := os.Stat(filepath.Join(manual, "a.txt")); err != nil {
		t.Fatal("unowned worktree was changed")
	}
	for _, name := range []string{"../outside", "a/b", "a\\b", "..", "feature\nname"} {
		if _, _, err := invokeClaudeHook(t, bin, claudeHookInput{Cwd: r.dir, Name: name}, "create"); err == nil {
			t.Fatalf("accepted unsafe name %q", name)
		}
	}
}

func TestE2E_ClaudeHookFailureAndBaseRef(t *testing.T) {
	bin := buildGitCowWorktree(t)
	r := newRepo(t, "claude-failure")
	r.commit("a.txt", "remote default\n")
	remote := gitOutput(t, r.dir, "rev-parse", "HEAD")
	r.run("git", "update-ref", "refs/remotes/origin/main", remote)
	r.run("git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	r.commit("a.txt", "local changes\n")
	if out, diag, err := invokeClaudeHook(t, bin, claudeHookInput{Cwd: r.dir, Name: "cached-default"}, "create"); err != nil {
		t.Fatalf("create: %v\n%s", err, diag)
	} else if got := gitOutput(t, strings.TrimSpace(out), "rev-parse", "HEAD"); got != remote {
		t.Fatalf("default base = %s, want cached origin/HEAD %s", got, remote)
	}
	hook := "#!/bin/sh\nexit 71\n"
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, _, err := invokeClaudeHook(t, bin, claudeHookInput{Cwd: r.dir, Name: "failed"}, "create"); err == nil || out != "" {
		t.Fatalf("failed hook returned a path: %q, %v", out, err)
	}
	path := filepath.Join(r.dir, ".claude", "worktrees", "failed")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed hook left worktree: %v", err)
	}
	branches, _ := branchHeads(r.dir)
	if _, remains := branches["refs/heads/worktree-failed"]; remains {
		t.Fatal("failed hook left its branch")
	}
}

func invokeClaudeHook(t *testing.T, bin string, input claudeHookInput, args ...string) (string, string, error) {
	t.Helper()
	return invokeClaudeHookFrom(t, bin, "", input, args...)
}

func invokeClaudeHookFrom(t *testing.T, bin, cwd string, input claudeHookInput, args ...string) (string, string, error) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, append([]string{"claude-hook"}, args...)...)
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(data)
	var out, diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostics
	err = cmd.Run()
	return out.String(), diagnostics.String(), err
}

func TestE2E_ClaudeHookFromLinkedWorktree(t *testing.T) {
	bin := buildGitCowWorktree(t)
	r := newRepo(t, "claude-linked-cwd")
	r.commit("a.txt", "main contents\n")
	r.commit(".gitignore", ".claude/worktrees/\nlocal.env\n")
	source := filepath.Join(r.parent, "source-linked")
	r.run("git", "worktree", "add", "-b", "source-branch", source, "HEAD")
	gitOutput(t, source, "commit", "--allow-empty", "-m", "linked checkout commit")
	head := gitOutput(t, source, "rev-parse", "HEAD")
	for name, content := range map[string]string{".worktreeinclude": "local.env\n", "local.env": "linked local contents\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in := claudeHookInput{Cwd: source, Name: "from-linked", Event: "WorktreeCreate"}
	out, diag, err := invokeClaudeHookFrom(t, bin, source, in, "create", "--base-ref", "HEAD", "--copy-local")
	if err != nil {
		t.Fatalf("create from linked cwd: %v\n%s", err, diag)
	}
	path := strings.TrimSpace(out)
	want, _ := absClean(filepath.Join(r.dir, ".claude", "worktrees", in.Name))
	if path != want || gitOutput(t, path, "rev-parse", "HEAD") != head {
		t.Fatalf("created %s; want main-checkout destination %s and caller's HEAD %s", path, want, head)
	}
	if data, err := os.ReadFile(filepath.Join(path, "local.env")); err != nil || string(data) != "linked local contents\n" {
		t.Fatalf("caller-local file not copied: %q, %v", data, err)
	}
	// Both the JSON cwd and the process cwd can be inside the target. Local
	// edits require explicit force, so first verify refusal.
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("local edits\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(path, "subdir")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	remove := claudeHookInput{Cwd: cwd, WorktreePath: path, Event: "WorktreeRemove"}
	if _, _, err := invokeClaudeHookFrom(t, bin, cwd, remove, "remove"); err == nil {
		t.Fatal("removed dirty worktree from its own cwd without --force")
	}
	if _, diag, err := invokeClaudeHookFrom(t, bin, cwd, remove, "remove", "--force"); err != nil {
		t.Fatalf("remove from linked process/JSON cwd: %v\n%s", err, diag)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed worktree directory still exists: %v", err)
	}
	wts, err := listWorktrees(r.dir)
	if err != nil || len(wts) != 2 {
		t.Fatalf("removed worktree still registered: %v, %v", wts, err)
	}
	branches, err := branchHeads(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, remains := branches["refs/heads/worktree-from-linked"]; remains {
		t.Fatal("unchanged owned branch was not removed")
	}
}

func TestE2E_ClaudeCopyKeepsTrackedTarget(t *testing.T) {
	bin := buildGitCowWorktree(t)
	r := newRepo(t, "copy-tracked")
	r.commit(".env", "tracked target contents\n")
	r.branch("tracked-env", "HEAD")
	r.rm(".env")
	r.commitAll("remove tracked env")
	r.commit(".gitignore", ".env\n.claude/worktrees/\n")
	for name, content := range map[string]string{".worktreeinclude": ".env\n", ".env": "ignored local contents\n"} {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, diag, err := invokeClaudeHook(t, bin, claudeHookInput{Cwd: r.dir, Name: "tracked-target"}, "create", "--base-ref", "tracked-env", "--copy-local")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, diag)
	}
	path := strings.TrimSpace(out)
	data, err := os.ReadFile(filepath.Join(path, ".env"))
	if err != nil || string(data) != "tracked target contents\n" {
		t.Fatalf("overwrote tracked target: %q, %v", data, err)
	}
	if got := gitOutput(t, path, "status", "--porcelain"); got != "" {
		t.Fatalf("tracked target dirty: %s", got)
	}
}

func TestE2E_Help(t *testing.T) {
	bin := buildGitCowWorktree(t)
	if out, err := exec.Command(bin, "--help").CombinedOutput(); err != nil || !strings.Contains(string(out), "claude-hook") {
		t.Fatalf("root help: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "add", "-h")
	out, err := cmd.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 129 {
		t.Fatalf("add help exit: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "missing <path>") || !strings.Contains(string(out), "git worktree add") {
		t.Fatalf("add help missing: %s", out)
	}
}

func TestClaudeHookRejectsInvalidInput(t *testing.T) {
	for _, data := range []string{"{} {}", "{", strings.Repeat(" ", (1<<20)+1), `{"cwd":"relative","name":"feature"}`} {
		var out, diagnostics bytes.Buffer
		if err := runClaudeHook([]string{"create"}, strings.NewReader(data), &out, &diagnostics); err == nil {
			t.Fatal("invalid hook input accepted")
		}
		if out.Len() != 0 {
			t.Fatalf("invalid input emitted a worktree path: %q", out.String())
		}
	}
}
