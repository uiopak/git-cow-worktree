package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// createdWorktree records what this invocation created. Cleanup checks the
// Git pointer and registration before asking Git to remove the worktree.
type createdWorktree struct {
	repo, path, gitDir, head string
	gitFile                  []byte
	gitFileInfo              fs.FileInfo
	oldBranches              map[string]string
	oldDirMode               fs.FileMode
	hadDir                   bool
	ready, materialized      bool
}

func (c *createdWorktree) prepare(repo, path string) error {
	c.repo, c.path = repo, path
	st, err := os.Lstat(path)
	if err == nil {
		c.hadDir, c.oldDirMode = st.IsDir(), st.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	c.oldBranches, err = branchHeads(repo)
	return err
}

func branchHeads(repo string) (map[string]string, error) {
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("snapshot branches: %w", err)
	}
	branches := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		ref, oid, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			branches[ref] = oid
		}
	}
	return branches, nil
}

func (c *createdWorktree) remember() error {
	path := filepath.Join(c.path, ".git")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("record new worktree: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("new worktree has no regular .git pointer: %s", c.path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dir, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	if !ok || dir == "" {
		return fmt.Errorf("invalid new worktree .git pointer: %s", c.path)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.path, dir)
	}
	c.gitDir, err = absClean(dir)
	if err != nil {
		return err
	}
	c.gitFile, c.gitFileInfo, c.ready = data, info, true
	return nil
}

func (c *createdWorktree) rollback(streams commandIO) error {
	if !c.ready {
		return nil
	}
	info, err := os.Lstat(filepath.Join(c.path, ".git"))
	if err != nil || !os.SameFile(c.gitFileInfo, info) {
		return fmt.Errorf("cleanup left %s in place: its Git pointer changed", c.path)
	}
	data, err := os.ReadFile(filepath.Join(c.path, ".git"))
	if err != nil || !bytes.Equal(data, c.gitFile) {
		return fmt.Errorf("cleanup left %s in place: its Git pointer changed", c.path)
	}
	worktrees, err := listWorktrees(c.repo)
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	var owned *Worktree
	for i := range worktrees {
		if samePath(worktrees[i].Path, c.path) && !worktrees[i].IsMain {
			owned = &worktrees[i]
			break
		}
	}
	if owned == nil || c.head != "" && owned.HEAD != c.head {
		return fmt.Errorf("cleanup left %s in place: its registration or HEAD changed", c.path)
	}
	// Both force flags are needed if this invocation requested --lock.
	if err := runGitStreamingIO(streams, c.repo, "worktree", "remove", "--force", "--force", "--", c.path); err != nil {
		return fmt.Errorf("cleanup worktree %s: %w", c.path, err)
	}
	c.ready = false
	if c.hadDir {
		if err := os.Mkdir(c.path, c.oldDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("restore existing empty directory: %w", err)
		}
	}
	if owned.Branch != "" {
		if !branchWasPresent(c.oldBranches, owned.Branch) {
			return deleteUnchangedBranch(streams, c.repo, owned.Branch, owned.HEAD)
		}
	}
	return nil
}

func branchWasPresent(branches map[string]string, ref string) bool {
	if _, ok := branches[ref]; ok {
		return true
	}
	if runtime.GOOS == "windows" {
		for old := range branches {
			if strings.EqualFold(old, ref) {
				return true
			}
		}
	}
	return false
}

func deleteUnchangedBranch(streams commandIO, repo, ref, oid string) error {
	if !strings.HasPrefix(ref, "refs/heads/") || oid == "" {
		return errors.New("refusing to delete an unidentified branch")
	}
	worktrees, err := listWorktrees(repo)
	if err != nil {
		return err
	}
	for _, wt := range worktrees {
		if wt.Branch == ref {
			return fmt.Errorf("kept branch %s: checked out in %s", ref, wt.Path)
		}
	}
	// Compare-and-delete preserves a branch that acquired commits meanwhile.
	if err := runGitStreamingIO(streams, repo, "update-ref", "-d", ref, oid); err != nil {
		return fmt.Errorf("kept branch %s: %w", ref, err)
	}
	return nil
}
