package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git evaluates .worktreeinclude as ignore patterns, then check-ignore
// restricts that list to files the source repository actually ignores.
func claudeLocalFiles(repo string) ([]string, error) {
	include := filepath.Join(repo, ".worktreeinclude")
	info, err := os.Lstat(include)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New(".worktreeinclude must be a regular file")
	}
	cmd := exec.Command("git", "ls-files", "--others", "--ignored", "-z", "--exclude-from="+include)
	cmd.Dir = repo
	candidates, err := cmd.Output()
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	cmd = exec.Command("git", "check-ignore", "--stdin", "-z")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(string(candidates))
	ignored, err := cmd.Output()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	wts, err := listWorktrees(repo)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range strings.Split(strings.TrimRight(string(ignored), "\x00"), "\x00") {
		if path == "" {
			continue
		}
		full := filepath.Join(repo, filepath.FromSlash(path))
		nested := false
		for _, wt := range wts {
			if !samePath(wt.Path, repo) && pathHasPrefix(full, wt.Path) {
				nested = true
				break
			}
		}
		if !nested {
			files = append(files, path)
		}
	}
	return files, nil
}

func copyClaudeLocalFiles(repo, target string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	source, err := os.OpenRoot(repo)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer dest.Close()
	for _, name := range paths {
		path := filepath.FromSlash(name)
		if !filepath.IsLocal(path) || pathHasPrefix(filepath.Join(repo, path), filepath.Join(repo, ".git")) {
			return fmt.Errorf("refusing local-file path %q", name)
		}
		if err := copyClaudeFile(source, dest, path); err != nil {
			return fmt.Errorf("copy local file %s: %w", name, err)
		}
	}
	return nil
}

func copyClaudeFile(source, dest *os.Root, path string) (err error) {
	for current := path; current != "."; current = filepath.Dir(current) {
		for _, root := range []*os.Root{source, dest} {
			info, statErr := root.Lstat(current)
			if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return errors.New("local-file copying does not follow symlinks")
			}
			if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
				return statErr
			}
		}
	}
	f, err := source.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("local-file copying requires regular files")
	}
	if err := dest.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := dest.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if errors.Is(err, fs.ErrExist) {
		return nil // Never overwrite a file checked out from the target ref.
	}
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, f)
	return errors.Join(copyErr, out.Close())
}
