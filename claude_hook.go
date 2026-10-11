package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const claudeMarkerName = "git-cow-claude.json"

var claudeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type claudeHookInput struct {
	Cwd          string `json:"cwd"`
	Name         string `json:"name"`
	WorktreePath string `json:"worktree_path"`
	Event        string `json:"hook_event_name"`
}

type claudeMarker struct {
	Version     int    `json:"version"`
	CommonDir   string `json:"common_dir"`
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	InitialHead string `json:"initial_head"`
}

func runClaudeHook(args []string, input io.Reader, output, diagnostics io.Writer) error {
	if len(args) == 0 || args[0] != "create" && args[0] != "remove" {
		return errors.New("usage: claude-hook create [--base-ref <ref>] [--root <path>] [--copy-local] | remove [--force]")
	}
	flags := flag.NewFlagSet("claude-hook "+args[0], flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	var base, root string
	var copyLocal, force bool
	if args[0] == "create" {
		flags.StringVar(&base, "base-ref", "", "base commit (default: cached origin/HEAD, then HEAD)")
		flags.StringVar(&root, "root", "", "absolute worktree parent directory (default: <repo>/.claude/worktrees)")
		flags.BoolVar(&copyLocal, "copy-local", false, "copy ignored files matched by .worktreeinclude")
	} else {
		flags.BoolVar(&force, "force", false, "allow Git to remove a dirty worktree")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected claude-hook argument")
	}
	var in claudeHookInput
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("Claude hook input exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&in); err != nil {
		return fmt.Errorf("read Claude hook input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("Claude hook input must contain one JSON object")
	}
	if in.Cwd == "" || !filepath.IsAbs(in.Cwd) {
		return errors.New("Claude hook cwd must be an absolute repository path")
	}
	expected := "WorktreeCreate"
	if args[0] == "remove" {
		expected = "WorktreeRemove"
	}
	if in.Event != "" && in.Event != expected {
		return fmt.Errorf("expected %s input, got %s", expected, in.Event)
	}
	repo, err := gitText(in.Cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	repo, err = absClean(repo)
	if err != nil {
		return err
	}
	common, err := gitCommonDir(repo)
	if err != nil {
		return err
	}
	mainRepo, err := claudeMainWorktree(repo)
	if err != nil {
		return err
	}
	streams := commandIO{out: diagnostics, err: diagnostics}
	if args[0] == "remove" {
		return removeClaudeWorktree(in, mainRepo, common, force, streams)
	}
	if root == "" {
		root = filepath.Join(mainRepo, ".claude", "worktrees")
	}
	// Resolve HEAD and select local files in the caller's checkout, even when
	// the default destination belongs to the main checkout.
	return createClaudeWorktree(in, repo, common, base, root, copyLocal, output, streams)
}

func claudeMainWorktree(repo string) (string, error) {
	wts, err := listWorktrees(repo)
	if err != nil {
		return "", err
	}
	for _, wt := range wts {
		if wt.IsMain && !wt.Bare {
			return absClean(wt.Path)
		}
	}
	return "", errors.New("Claude hooks require a repository with a main checkout")
}

func gitText(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitCommonDir(repo string) (string, error) {
	path, err := gitText(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return absClean(path)
}

func createClaudeWorktree(in claudeHookInput, repo, common, base, root string, copyLocal bool, output io.Writer, streams commandIO) (err error) {
	if !claudeName.MatchString(in.Name) || !filepath.IsLocal(in.Name) || strings.HasSuffix(in.Name, ".") {
		return errors.New("Claude worktree name must be a single name containing letters, digits, '.', '_' or '-'")
	}
	if !filepath.IsAbs(root) {
		return errors.New("--root must be an absolute path")
	}
	path := filepath.Join(root, in.Name)
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	path, err = absClean(path)
	if err != nil {
		return err
	}
	branch := "refs/heads/worktree-" + in.Name
	if _, err := gitText(repo, "check-ref-format", branch); err != nil {
		return fmt.Errorf("invalid Claude branch name: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(filepath.Dir(path), ".git-cow-"+in.Name+".lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("Claude worktree name is reserved by %s; if its creator is no longer running, remove this stale lock and retry", lockPath)
		}
		return fmt.Errorf("reserve Claude worktree name: %w", err)
	}
	if err := lock.Close(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(lockPath)) }()
	if _, statErr := os.Lstat(path); statErr == nil {
		marker, _, err := readClaudeMarker(repo, common, path)
		if err != nil {
			return fmt.Errorf("refusing to reuse existing directory %s: %w", path, err)
		}
		if marker.Branch != branch {
			return errors.New("existing Claude worktree belongs to a different branch")
		}
		_, err = fmt.Fprintln(output, path)
		return err
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	var localFiles []string
	if copyLocal {
		localFiles, err = claudeLocalFiles(repo)
		if err != nil {
			return err
		}
	}
	if base == "" {
		base = "origin/HEAD"
		if _, err := resolveRev(repo, base); err != nil {
			base = "HEAD"
		}
	}
	// Resolve before creating anything so option-like refs cannot reach add.
	sha, err := resolveRev(repo, base)
	if err != nil {
		return err
	}
	created := &createdWorktree{}
	defer func() {
		if err != nil && created.ready {
			err = errors.Join(err, created.rollback(streams))
		}
	}()
	if err = runWithRepo([]string{"-b", strings.TrimPrefix(branch, "refs/heads/"), "--", path, sha}, streams, created, repo); err != nil {
		return err
	}
	if err = copyClaudeLocalFiles(repo, path, localFiles); err != nil {
		return err
	}
	marker := claudeMarker{Version: 1, CommonDir: common, Path: path, Branch: branch, InitialHead: created.head}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(created.gitDir, claudeMarkerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	if err = errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, path)
	return err
}

func readClaudeMarker(repo, common, path string) (claudeMarker, string, error) {
	var marker claudeMarker
	if err := rejectSymlinkComponents(path); err != nil {
		return marker, "", err
	}
	path, err := absClean(path)
	if err != nil {
		return marker, "", err
	}
	wts, err := listWorktrees(repo)
	if err != nil {
		return marker, "", err
	}
	var wt *Worktree
	for i := range wts {
		if !wts[i].IsMain && samePath(wts[i].Path, path) {
			wt = &wts[i]
			break
		}
	}
	if wt == nil {
		return marker, "", errors.New("path is not a registered linked worktree of this repository")
	}
	actualCommon, err := gitCommonDir(path)
	if err != nil || !samePath(actualCommon, common) {
		return marker, "", errors.New("worktree points at another repository")
	}
	gitDir, err := gitText(path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return marker, "", err
	}
	data, err := os.ReadFile(filepath.Join(gitDir, claudeMarkerName))
	if err != nil {
		return marker, "", fmt.Errorf("no git-cow Claude ownership record: %w", err)
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return marker, "", err
	}
	if marker.Version != 1 || !samePath(marker.CommonDir, common) || !samePath(marker.Path, path) || !strings.HasPrefix(marker.Branch, "refs/heads/worktree-") || marker.InitialHead == "" {
		return marker, "", errors.New("invalid git-cow Claude ownership record")
	}
	return marker, wt.HEAD, nil
}

func removeClaudeWorktree(in claudeHookInput, repo, common string, force bool, streams commandIO) error {
	if in.WorktreePath == "" || !filepath.IsAbs(in.WorktreePath) {
		return errors.New("Claude worktree_path must be an absolute path")
	}
	if _, err := os.Lstat(in.WorktreePath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	marker, head, err := readClaudeMarker(repo, common, in.WorktreePath)
	if err != nil {
		return fmt.Errorf("refusing to remove %s: %w", in.WorktreePath, err)
	}
	// A Windows process holds its current directory open. Move this adapter as
	// well as Git outside the target before removal, including subdirectory cwd.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if pathHasPrefix(cwd, marker.Path) {
		if err := os.Chdir(repo); err != nil {
			return err
		}
		defer func() {
			// A refused removal leaves the old cwd available. A successful one
			// deliberately leaves this short-lived CLI in the main checkout.
			if _, err := os.Stat(cwd); err == nil {
				_ = os.Chdir(cwd)
			}
		}()
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", marker.Path)
	if err := runGitStreamingIO(streams, repo, args...); err != nil {
		return fmt.Errorf("remove Claude worktree: %w", err)
	}
	branches, err := branchHeads(repo)
	if err != nil {
		return err
	}
	if head != marker.InitialHead || branches[marker.Branch] != marker.InitialHead {
		fmt.Fprintf(streams.err, "%s: kept branch %s because its tip changed\n", commandName, marker.Branch)
		return nil
	}
	return deleteUnchangedBranch(streams, repo, marker.Branch, marker.InitialHead)
}

func rejectSymlinkComponents(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlinked worktree path: %s", current)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
