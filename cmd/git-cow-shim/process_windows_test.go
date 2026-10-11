//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsJobKillsChildAndGrandchild(t *testing.T) {
	pids := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	companionDir := t.TempDir()
	copyExecutable(t, self, filepath.Join(companionDir, "git-cow-worktree.exe"))
	env := withEnv(cleanEnv(t), realGitEnv, realGit(t))
	env = withEnv(env, "PATH", companionDir+string(os.PathListSeparator)+envValue(env, "PATH"))
	env = withEnv(env, "GIT_COW_TEST_HELPER", "tree-child")
	env = withEnv(env, "GIT_COW_TEST_PIDS", pids)
	env = withEnv(env, "GIT_COW_SHIM_TRACE", "1")
	cmd := exec.Command(shimBinary, "worktree", "add", "target", "HEAD")
	cmd.Env = env
	stderr, err := os.Create(filepath.Join(pids, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	// Hold process handles before killing the shim so PID reuse cannot make the
	// test accidentally observe or terminate another process.
	var processes []windows.Handle
	defer func() {
		for _, process := range processes {
			windows.TerminateProcess(process, 1)
			windows.CloseHandle(process)
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for _, name := range []string{"child", "grandchild"} {
		var data []byte
		for time.Now().Before(deadline) {
			data, err = os.ReadFile(filepath.Join(pids, name))
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(pids, "stderr"))
			t.Fatalf("helper %s did not become ready: %v\n%s", name, err, log)
		}
		pid, err := strconv.ParseUint(string(data), 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
		if err != nil {
			t.Fatal(err)
		}
		processes = append(processes, process)
		if state, err := windows.WaitForSingleObject(process, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
			t.Fatalf("helper %s was not alive before cancellation: state=%d err=%v", name, state, err)
		}
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	for i, process := range processes {
		if state, err := windows.WaitForSingleObject(process, 10000); err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("helper %d survived shim cancellation: state=%d err=%v", i, state, err)
		}
	}
}

func TestWindowsOrdinaryCommandPreservesBackgroundChild(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		t.Run(strconv.FormatBool(bypass), func(t *testing.T) {
			pids := t.TempDir()
			env := withEnv(helperEnv(t, "background-parent"), "GIT_COW_TEST_PIDS", pids)
			if bypass {
				env = withEnv(env, "GIT_COW_SHIM_BYPASS", "1")
			}
			_, stderr, code := capture(t, shimBinary, []string{"status"}, env, "", "")
			if code != 0 {
				t.Fatalf("ordinary command: exit=%d stderr=%q", code, stderr)
			}
			deadline := time.Now().Add(10 * time.Second)
			var data []byte
			var err error
			for time.Now().Before(deadline) {
				data, err = os.ReadFile(filepath.Join(pids, "grandchild"))
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatalf("background child did not start: %v", err)
			}
			pid, err := strconv.ParseUint(string(data), 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
			if err != nil {
				t.Fatalf("background child did not survive shim exit: %v", err)
			}
			defer windows.CloseHandle(process)
			defer func() {
				windows.TerminateProcess(process, 1)
				windows.WaitForSingleObject(process, 10000)
			}()
			if state, err := windows.WaitForSingleObject(process, 200); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
				t.Fatalf("background child did not survive shim exit: state=%d err=%v", state, err)
			}
		})
	}
}

func TestWindowsOrdinaryFsmonitorSurvives(t *testing.T) {
	git := realGit(t)
	repo := t.TempDir()
	env := withEnv(cleanEnv(t), realGitEnv, git)
	_, stderr, code := capture(t, git, []string{"init", repo}, env, "", "")
	if code != 0 {
		t.Fatalf("init fsmonitor fixture: exit=%d stderr=%q", code, stderr)
	}
	// Register cleanup before any command can start this test-owned daemon.
	supported := false
	t.Cleanup(func() {
		_, stderr, code := capture(t, git, []string{"fsmonitor--daemon", "stop"}, env, repo, "")
		if code != 0 && supported {
			t.Errorf("stop test-owned fsmonitor: exit=%d stderr=%q", code, stderr)
		}
	})
	_, stderr, code = capture(t, git, []string{"-c", "core.fsmonitor=true", "status", "--porcelain"}, env, repo, "")
	if code != 0 {
		t.Skipf("real Git fsmonitor startup unsupported: exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code = capture(t, git, []string{"fsmonitor--daemon", "status"}, env, repo, "")
	if code != 0 {
		t.Skipf("real Git does not support a persistent fsmonitor here: exit=%d stderr=%q", code, stderr)
	}
	supported = true
	_, stderr, code = capture(t, git, []string{"fsmonitor--daemon", "stop"}, env, repo, "")
	if code != 0 {
		t.Fatalf("stop baseline fsmonitor: exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code = capture(t, shimBinary, []string{"-c", "core.fsmonitor=true", "status", "--porcelain"}, env, repo, "")
	if code != 0 {
		t.Fatalf("shim fsmonitor startup: exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code = capture(t, git, []string{"fsmonitor--daemon", "status"}, env, repo, "")
	if code != 0 {
		t.Fatalf("ordinary shim command killed fsmonitor: exit=%d stderr=%q", code, stderr)
	}
}

func TestWindowsMissingStandardHandles(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := withEnv(cleanEnv(t), realGitEnv, realGit(t))
	env = withEnv(env, "GIT_COW_TEST_HELPER", "missing-handles")
	// The helper closes all three handles before invoking the shim's run path.
	// Git must read EOF from NUL, write its hash to NUL, and exit successfully.
	_, stderr, code := capture(t, self, nil, env, "", "")
	if code != 0 {
		t.Fatalf("missing standard handles: exit=%d stderr=%q", code, stderr)
	}
}

func TestWindowsEnvironmentBlock(t *testing.T) {
	block, err := environmentBlock([]string{"z=日本", "A=first", "=C:=C:\\current"})
	if err != nil {
		t.Fatal(err)
	}
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatal("missing environment double terminator")
	}
	if _, err := environmentBlock([]string{"bad=nul\x00"}); err == nil {
		t.Fatal("accepted NUL in environment")
	}
}

func TestWindowsFixtureFilesystem(t *testing.T) {
	root, err := windows.UTF16PtrFromString(filepath.VolumeName(t.TempDir()) + `\`)
	if err != nil {
		t.Fatal(err)
	}
	var name [64]uint16
	var flags uint32
	if err := windows.GetVolumeInformation(root, nil, 0, nil, nil, &flags, &name[0], uint32(len(name))); err != nil {
		t.Fatal(err)
	}
	t.Logf("repository fixtures use %s, filesystem flags=%#x; repository tests exercise the native copy fallback when block cloning is unavailable", windows.UTF16ToString(name[:]), flags)
}
