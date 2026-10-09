package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestClonePlanNestedGitPaths(t *testing.T) {
	entry := TreeEntry{Mode: "100644", SHA: "blob"}
	tree := &treeIndex{
		Trees: map[string]string{"a": "tree-a", "a/b": "tree-b", "a/b/c": "tree-c"},
		Blobs: map[string]TreeEntry{
			"root.txt":        entry,
			"a/b/c/clean.txt": entry,
			"a/b/c/dirty.txt": entry,
		},
	}
	excluded := map[string]bool{"a/b/c/dirty.txt": true}
	plan := planClones(tree, tree, excluded, true)
	if len(plan.Dirs) != 0 || !equalStringSlices(sortedKeys(plan.coverage().files), []string{"a/b/c/clean.txt", "root.txt"}) {
		t.Fatalf("nested dirty path should prevent directory clones: %+v", plan)
	}
	for _, dir := range []string{"a", "a/b", "a/b/c"} {
		if !taint(excluded)[dir] {
			t.Errorf("missing tainted parent %q", dir)
		}
	}
	coverage := (clonePlan{Dirs: []string{"a/b"}}).coverage()
	if !coverage.covers("a/b/c/clean.txt") || coverage.covers("a/b-other/file.txt") {
		t.Fatal("directory coverage must follow slash-separated Git paths")
	}
	got := parentDirs([]string{"a/b/c/clean.txt"})
	slices.Sort(got)
	if !equalStringSlices(got, []string{"a", "a/b", "a/b/c"}) {
		t.Fatalf("parent directories = %v", got)
	}
}

func TestWindowsWorktreePathCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths are case insensitive")
	}
	root := t.TempDir()
	if !samePath(root, strings.ToUpper(root)) {
		t.Fatal("samePath should accept a different Windows path case")
	}
	if !pathHasPrefix(filepath.Join(strings.ToUpper(root), "child"), root) {
		t.Fatal("pathHasPrefix should accept a different Windows path case")
	}
	if pathHasPrefix(root+"-other", root) {
		t.Fatal("pathHasPrefix must check directory boundaries")
	}
	parsed := parseWorktreesPorcelain("worktree " + filepath.ToSlash(root) + "\x00HEAD abc\x00\x00")
	if len(parsed) != 1 || parsed[0].Path != filepath.Clean(root) {
		t.Fatalf("worktree paths should use native separators: %+v", parsed)
	}
}

func TestValidateWindowsExecutableTreeEntry(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows has no executable permission bits")
	}
	root := t.TempDir()
	name := "program.sh"
	if err := os.WriteFile(filepath.Join(root, name), []byte("echo hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	oid, err := hashBlob(filepath.Join(root, name), 11, 20, buf)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := validateOne(root, name, TreeEntry{Mode: "100755", SHA: bytesToHex(oid)}, buf)
	if !ok || entry.Mode != 0o100755 {
		t.Fatalf("executable Git mode must survive Windows validation: ok=%v entry=%+v", ok, entry)
	}
}
