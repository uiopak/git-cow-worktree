package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var testBin string
var shimBinary string
var buildOnce sync.Once
var buildErr error

func TestMain(m *testing.M) {
	// Helper grandchildren are invoked without Go's -test flags.
	if mode := os.Getenv("GIT_COW_TEST_HELPER"); mode != "" {
		helperProcess(mode)
		os.Exit(0)
	}
	var err error
	testBin, err = os.MkdirTemp("", "git-cow-shim-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shimBinary = filepath.Join(testBin, executableName("git-cow-shim"))
	cmd := exec.Command("go", "build", "-o", shimBinary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build shim: %v\n%s", err, out)
		os.RemoveAll(testBin)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(testBin)
	os.Exit(code)
}

type helperReport struct {
	Args      []string
	Input     string
	Cwd       string
	Real      string
	Depth     string
	Bypass    string
	Config    string
	Path      string
	TraceFile string
}

func helperProcess(mode string) {
	switch mode {
	case "stdio", "cow-report", "launcher":
		var input []byte
		if mode != "launcher" {
			input, _ = io.ReadAll(os.Stdin)
		}
		cwd, _ := os.Getwd()
		report := helperReport{os.Args[1:], string(input), cwd, os.Getenv(realGitEnv), os.Getenv(depthEnv), os.Getenv("GIT_COW_SHIM_BYPASS"), os.Getenv("GIT_CONFIG_PARAMETERS"), os.Getenv("PATH"), os.Getenv(traceFileEnv)}
		if mode == "launcher" {
			data, _ := json.Marshal(report)
			if err := os.WriteFile(os.Getenv("GIT_COW_TEST_REPORT"), data, 0600); err != nil {
				os.Exit(1)
			}
			return
		}
		json.NewEncoder(os.Stdout).Encode(report)
		fmt.Fprint(os.Stderr, "helper stderr\n")
		if mode == "stdio" {
			os.Exit(37)
		}
	case "trace-observe":
		data, err := os.ReadFile(os.Getenv(traceFileEnv))
		if err != nil {
			os.Exit(2)
		}
		os.Stdout.Write(data)
	case "nested":
		cmd := exec.Command(os.Getenv("GIT_COW_TEST_PROXY"), "worktree", "add", "nested path", "HEAD")
		cmd.Env = withEnv(os.Environ(), "GIT_COW_TEST_HELPER", "stdio")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			os.Exit(1)
		}
	case "missing-handles":
		os.Stdin.Close()
		os.Stdout.Close()
		os.Stderr.Close()
		os.Exit(run(os.Args[0], []string{"hash-object", "--stdin"}, os.Environ()))
	case "tree-child", "background-parent":
		self, _ := os.Executable()
		cmd := exec.Command(self)
		cmd.Env = withEnv(os.Environ(), "GIT_COW_TEST_HELPER", "tree-grandchild")
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if mode == "background-parent" {
			return
		}
		writePID("child")
		for {
			time.Sleep(time.Second)
		}
	case "tree-grandchild":
		writePID("grandchild")
		for {
			time.Sleep(time.Second)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown helper", mode)
		os.Exit(2)
	}
}

func writePID(name string) {
	if err := os.WriteFile(filepath.Join(os.Getenv("GIT_COW_TEST_PIDS"), name), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		os.Exit(2)
	}
}

func cleanEnv(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_COW_") || strings.HasPrefix(upper, "GIT_CONFIG_") || upper == "GIT_DIR" || upper == "GIT_WORK_TREE" || upper == "GIT_COMMON_DIR" || upper == "GIT_EXEC_PATH" || upper == "GIT_NAMESPACE" {
			continue
		}
		env = append(env, entry)
	}
	env = withEnv(env, "GIT_CONFIG_NOSYSTEM", "1")
	env = withEnv(env, "GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	return env
}

func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		t.Fatal(err)
	}
}

func helperEnv(t *testing.T, mode string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := withEnv(cleanEnv(t), realGitEnv, self)
	return withEnv(env, "GIT_COW_TEST_HELPER", mode)
}

func capture(t *testing.T, exe string, args, env []string, cwd, input string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Env, cmd.Dir, cmd.Stdin = env, cwd, strings.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return out.String(), stderr.String(), exit.ExitCode()
		}
		t.Fatalf("run %s: %v", exe, err)
	}
	return out.String(), stderr.String(), 0
}

func TestProxyStreamsArgsExitAndCwd(t *testing.T) {
	args := []string{"status", "space and 日本", `quotes" and slash\`, "", "--no-checkout"}
	cwd := t.TempDir()
	out, stderr, code := capture(t, shimBinary, args, helperEnv(t, "stdio"), cwd, "stdin Ω\n")
	if code != 37 || stderr != "helper stderr\n" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr, out)
	}
	var report helperReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Args, args) || report.Input != "stdin Ω\n" || report.Cwd != cwd || report.Depth != "1" || report.Bypass != "1" || !filepath.IsAbs(report.Real) {
		t.Fatalf("report=%+v", report)
	}
}

func TestCompanionAndGitVersion(t *testing.T) {
	out, stderr, code := capture(t, shimBinary, []string{"--version"}, cleanEnv(t), "", "")
	if code != 0 || stderr != "" || !strings.HasPrefix(out, "git-cow-shim ") {
		t.Fatalf("companion version: %d %q %q", code, out, stderr)
	}
	proxy := filepath.Join(t.TempDir(), executableName("git"))
	copyExecutable(t, shimBinary, proxy)
	out, _, code = capture(t, proxy, []string{"--version"}, helperEnv(t, "stdio"), "", "")
	if code != 37 || !strings.Contains(out, `"--version"`) {
		t.Fatalf("git version did not proxy: %d %q", code, out)
	}
}

func TestProxyResolutionErrorsAndDepth(t *testing.T) {
	for _, item := range []struct{ key, value string }{
		{realGitEnv, "relative-git"},
		{realGitEnv, filepath.Join(t.TempDir(), "missing.exe")},
		{realGitEnv, shimBinary},
		{depthEnv, "8"},
		{depthEnv, "nonsense"},
	} {
		env := withEnv(helperEnv(t, "stdio"), item.key, item.value)
		out, stderr, code := capture(t, shimBinary, []string{"status"}, env, "", "")
		if code != 1 || out != "" || !strings.Contains(stderr, item.key) {
			t.Fatalf("%s=%q: %d %q %q", item.key, item.value, code, out, stderr)
		}
	}
}

func TestNestedShimBypassesCow(t *testing.T) {
	env := withEnv(helperEnv(t, "nested"), "GIT_COW_TEST_PROXY", shimBinary)
	env = withEnv(env, "GIT_COW_SHIM_TRACE", "1")
	out, stderr, code := capture(t, shimBinary, []string{"status"}, env, "", "")
	if code != 37 || !strings.Contains(stderr, "route=bypass") || strings.Contains(stderr, "route=cow ") {
		t.Fatalf("nested proxy: %d %q %q", code, out, stderr)
	}
	var report helperReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Depth != "2" || !reflect.DeepEqual(report.Args, []string{"worktree", "add", "nested path", "HEAD"}) {
		t.Fatalf("nested report=%+v", report)
	}
}

func TestCowUnavailablePassesThrough(t *testing.T) {
	env := withEnv(helperEnv(t, "stdio"), "PATH", testBin)
	env = withEnv(env, "GIT_COW_SHIM_TRACE", "1")
	out, stderr, code := capture(t, shimBinary, []string{"worktree", "add", "target", "HEAD"}, env, "", "")
	if code != 37 || !strings.Contains(stderr, "route=cow unavailable") || !strings.Contains(out, `"worktree"`) {
		t.Fatalf("fallback: %d %q %q", code, out, stderr)
	}
}

func realGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("real Git unavailable")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func cowBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testBin, "core", executableName("git-cow-worktree"))
	buildOnce.Do(func() {
		cmd := exec.Command("go", "build", "-o", path, "../..")
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build core: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return path
}

func TestGitOwnsCumulativeCAndConfig(t *testing.T) {
	git := realGit(t)
	dir := t.TempDir()
	self, _ := os.Executable()
	copyExecutable(t, self, filepath.Join(dir, executableName("git-cow-worktree")))
	parent := t.TempDir()
	child := filepath.Join(parent, "child 日本")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	env := withEnv(cleanEnv(t), realGitEnv, git)
	env = withEnv(env, "PATH", dir+string(os.PathListSeparator)+envValue(env, "PATH"))
	env = withEnv(env, "GIT_COW_TEST_HELPER", "cow-report")
	env = withEnv(env, "GIT_COW_SHIM_TRACE", "1")
	args := []string{"-C", parent, "-C", "child 日本", "-c", "checkout.workers=3", "-c", "cow.test=two words 日本", "worktree", "add", "-b", "branch", "path with spaces", "HEAD"}
	out, stderr, code := capture(t, shimBinary, args, env, "", "")
	if code != 0 || !strings.Contains(stderr, "route=cow ") {
		t.Fatalf("external dispatch: %d %q %q", code, out, stderr)
	}
	var report helperReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Cwd != child || !strings.Contains(report.Config, "checkout.workers") || !strings.Contains(report.Config, "two words 日本") || !reflect.DeepEqual(report.Args, []string{"add", "-b", "branch", "path with spaces", "HEAD"}) {
		t.Fatalf("external report=%+v", report)
	}
}

func TestCowFailureIsNotRetried(t *testing.T) {
	git := realGit(t)
	dir := t.TempDir()
	self, _ := os.Executable()
	copyExecutable(t, self, filepath.Join(dir, executableName("git-cow-worktree")))
	env := withEnv(cleanEnv(t), realGitEnv, git)
	env = withEnv(env, "PATH", dir+string(os.PathListSeparator)+envValue(env, "PATH"))
	env = withEnv(env, "GIT_COW_TEST_HELPER", "stdio")
	out, stderr, code := capture(t, shimBinary, []string{"worktree", "add", "target", "HEAD"}, env, dir, "")
	if code != 37 || stderr != "helper stderr\n" || !strings.Contains(out, `"add"`) {
		t.Fatalf("COW failure propagation: %d %q %q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "target")); !os.IsNotExist(err) {
		t.Fatalf("unexpected target after failed COW command: %v", err)
	}
}

func TestPortableRuntime(t *testing.T) {
	// This test also makes the platform expectations explicit in test output.
	t.Logf("native proxy process tests ran on %s/%s", runtime.GOOS, runtime.GOARCH)
}
