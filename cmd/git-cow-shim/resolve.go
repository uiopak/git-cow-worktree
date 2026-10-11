package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const realGitEnv = "GIT_COW_REAL_GIT"

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func executableFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("not an executable file")
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(path), ".exe") {
		return nil, errors.New("native .exe required")
	}
	return info, nil
}

func digest(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// SameFile catches hard links and aliases. Matching contents also catches copied
// git.exe shims in multiple application-specific PATH directories.
func sameShim(self string, own, candidate os.FileInfo, path string) bool {
	if os.SameFile(own, candidate) {
		return true
	}
	if own.Size() != candidate.Size() {
		return false
	}
	a, err := digest(self)
	if err != nil {
		return false
	}
	b, err := digest(path)
	return err == nil && a == b
}

func resolveRealGit(self, explicit, pathEnv string) (string, error) {
	own, err := os.Stat(self)
	if err != nil {
		return "", fmt.Errorf("inspect shim: %w", err)
	}
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", fmt.Errorf("%s must be an absolute executable path", realGitEnv)
		}
		info, err := executableFile(explicit)
		if err != nil {
			return "", fmt.Errorf("%s %q: %w", realGitEnv, explicit, err)
		}
		if sameShim(self, own, info, explicit) {
			return "", fmt.Errorf("%s points to the shim; refusing recursive Git resolution", realGitEnv)
		}
		return filepath.Clean(explicit), nil
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		// Ignore implicit/current-directory PATH entries, as exec.LookPath does.
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, executableName("git"))
		info, err := executableFile(candidate)
		if err == nil && !sameShim(self, own, info, candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("cannot find real Git on PATH; set GIT_COW_REAL_GIT to its absolute executable path")
}

func resolveCow(self, pathEnv string) string {
	dirs := append([]string{filepath.Dir(self)}, filepath.SplitList(pathEnv)...)
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, executableName("git-cow-worktree"))
		if _, err := executableFile(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if ok && (k == key || runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			return v
		}
	}
	return ""
}

func withEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		k, _, ok := strings.Cut(entry, "=")
		if ok && (k == key || runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, key+"="+value)
}
