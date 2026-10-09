package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestE2E_CheckoutConversions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	binary := buildGitCowWorktree(t)
	for _, name := range []string{"autocrlf", "target-adds-crlf", "target-changes-to-lf", "config-changes-to-crlf", "forced-text-with-nul", "text-input-with-autocrlf", "text-unset-value", "crlf-unset-value", "filter-unset-value", "attribute-tree", "attribute-source-env"} {
		t.Run(name, func(t *testing.T) {
			setup := func(label string) *repo {
				r := newRepo(t, label)
				if name == "target-changes-to-lf" {
					r.commit(".gitattributes", "*.txt text eol=crlf\n")
				}
				if name == "forced-text-with-nul" {
					r.commit(".gitattributes", "*.txt text eol=crlf\n")
				}
				content := "alpha\nbeta\n"
				if name == "forced-text-with-nul" {
					content = "alpha\x00beta\n"
				}
				r.commit("dir/file with space.txt", content)
				r.commit("dir/plain.bin", "binary\x00data\n")
				r.branch("base", "HEAD")
				if name == "autocrlf" {
					r.run("git", "config", "core.autocrlf", "true")
				}
				r.worktree("src", "base")
				switch name {
				case "target-adds-crlf":
					r.commit(".gitattributes", "*.txt text eol=crlf\n")
				case "target-changes-to-lf":
					r.commit(".gitattributes", "*.txt text eol=lf\n")
				case "config-changes-to-crlf":
					r.run("git", "config", "core.autocrlf", "true")
				case "text-input-with-autocrlf":
					r.commit(".gitattributes", "*.txt text=input\n")
					r.run("git", "config", "core.autocrlf", "true")
				case "text-unset-value":
					r.commit(".gitattributes", "*.txt text=unset\n")
					r.run("git", "config", "core.autocrlf", "true")
				case "crlf-unset-value":
					r.commit(".gitattributes", "*.txt crlf=unset\n")
					r.run("git", "config", "core.autocrlf", "true")
				case "filter-unset-value":
					r.run("git", "config", "filter.unset.smudge", "printf 'smudged\\n'")
					r.run("git", "config", "filter.unset.clean", "printf 'alpha\\nbeta\\n'")
					r.commit(".gitattributes", "*.txt filter=unset\n")
				case "attribute-tree", "attribute-source-env":
					r.commit(".gitattributes", "*.txt text eol=crlf\n")
					r.branch("attributes", "HEAD")
					if name == "attribute-tree" {
						r.run("git", "config", "attr.tree", "attributes")
					}
					r.branch("target", "base")
					return r
				}
				r.branch("target", "HEAD")
				return r
			}
			cowRepo, plainRepo := setup("cow"), setup("plain")
			if name == "attribute-source-env" {
				t.Setenv("GIT_ATTR_SOURCE", "attributes")
			}
			cowOut, plainOut := filepath.Join(cowRepo.parent, "cow-out"), filepath.Join(plainRepo.parent, "plain-out")
			source := filepath.Join(cowRepo.parent, "src")
			cmd := exec.Command(binary, "add", "-v", "--from", source, "--detach", cowOut, "target")
			cmd.Dir = cowRepo.dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git-cow-worktree add: %v\n%s", err, out)
			} else {
				t.Logf("%s", out)
			}
			plainRepo.run("git", "worktree", "add", "--detach", plainOut, "target")
			diffWorktrees(t, plainOut, cowOut)
			assertIndexUpToDate(t, cowOut, plainOut)
			if name == "autocrlf" && cowSupported(t, cowRepo.parent) {
				path := "dir/file with space.txt"
				got, err := os.ReadFile(filepath.Join(cowOut, path))
				if err != nil || !bytes.Equal(got, []byte("alpha\r\nbeta\r\n")) {
					t.Fatalf("expected CRLF checkout: content=%q err=%v", got, err)
				}
				srcInfo, err := os.Stat(filepath.Join(source, path))
				if err != nil {
					t.Fatal(err)
				}
				dstInfo, err := os.Stat(filepath.Join(cowOut, path))
				if err != nil {
					t.Fatal(err)
				}
				if !dstInfo.ModTime().Equal(srcInfo.ModTime()) {
					t.Fatal("checkout rewrote a valid CRLF clone")
				}
				assertShared(t, source, cowOut)
			}
		})
	}
}

func TestCheckoutConversionMismatchContinues(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := newRepo(t, "conversion-records")
	r.commit(".gitattributes", "*.txt text eol=crlf\n")
	wrong := bytes.Repeat([]byte("wrong\n"), 60_000)
	r.commit("bad.txt", string(wrong))
	r.commit("good.txt", "good\n")
	out := filepath.Join(r.parent, "out")
	r.run("git", "worktree", "add", "--no-checkout", "--detach", out, "HEAD")
	if err := os.WriteFile(filepath.Join(out, "bad.txt"), wrong, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "good.txt"), []byte("good\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := lsTree(out, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	entries := validateCheckoutConversions(out, tree, []string{"bad.txt", "good.txt"})
	if len(entries) != 1 || entries[0].Path != "good.txt" {
		t.Fatalf("a large mismatched record must not hide the following valid clone: %+v", entries)
	}
}

func TestCachedCheckoutKindsUseCommittedAttributes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := newRepo(t, "cached-attributes")
	r.commit(".gitattributes", "*.txt text eol=crlf\n")
	r.commit("file.txt", "content\n")
	if err := os.WriteFile(filepath.Join(r.dir, ".gitattributes"), []byte("*.txt -text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := gitOutput(t, r.dir, "ls-files", "--stage")
	kinds, ok := cachedCheckoutKinds(r.dir, []string{"file.txt"}, "false", false)
	if !ok || len(kinds) != 1 || kinds[0] != checkoutFiltered {
		t.Fatalf("cached attributes should read HEAD, not the dirty working tree: ok=%v kinds=%v", ok, kinds)
	}
	if after := gitOutput(t, r.dir, "ls-files", "--stage"); after != before {
		t.Fatal("cached attribute lookup changed the repository index")
	}
}
