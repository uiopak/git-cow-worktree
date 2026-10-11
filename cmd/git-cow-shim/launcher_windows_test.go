//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func psLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func TestT3LauncherCheckAndProcessEnvironment(t *testing.T) {
	git := realGit(t)
	cow := cowBinary(t)
	script, err := filepath.Abs(filepath.Join("..", "..", "integrations", "t3", "Start-T3WithCow.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	base := canonicalTempDir(t)
	proxyDir := filepath.Join(base, "dedicated shim Ω")
	reportFile := filepath.Join(base, "report.json")
	traceFile := filepath.Join(base, "trace 日本.jsonl")
	checkTrace := filepath.Join(base, "missing trace parent", "check.jsonl")
	args := []string{"space 日本", `quote"`, `C:\path with space\`, "", "literal $(never-run) `ticks` & ;"}
	var argumentLiterals []string
	for _, arg := range args {
		argumentLiterals = append(argumentLiterals, psLiteral(arg))
	}
	invocation := "& " + psLiteral(script) + " -RealGit " + psLiteral(git) + " -CowPath " + psLiteral(cow) + " -ShimPath " + psLiteral(shimBinary) + " -T3Path " + psLiteral(self) + " -ShimDirectory " + psLiteral(proxyDir) + " -T3Arguments @(" + strings.Join(argumentLiterals, ",") + ")"

	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable", shell)
			}
			// The helper mode is set only after launcher version checks. This runs a
			// deterministic test executable as T3 and never starts a live app.
			command := "$ErrorActionPreference='Stop'; " +
				"$names=@('PATH','GIT_COW_REAL_GIT','GIT_COW_SHIM_TRACE','GIT_COW_SHIM_TRACE_FILE','GIT_COW_SHIM_DEPTH','GIT_COW_SHIM_BYPASS'); $saved=@{}; " +
				"foreach($name in $names){$saved[$name]=[Environment]::GetEnvironmentVariable($name,'Process')}; " +
				"$check=" + invocation + " -CheckOnly -TraceFile " + psLiteral(filepath.Join("missing trace parent", "check.jsonl")) + "; " +
				"if ($check.TraceFile -cne " + psLiteral(checkTrace) + ") { throw 'CheckOnly did not canonicalize TraceFile' }; " +
				"if (Test-Path -LiteralPath " + psLiteral(proxyDir) + ") { throw 'CheckOnly created shim directory' }; " +
				"if (Test-Path -LiteralPath " + psLiteral(filepath.Dir(checkTrace)) + ") { throw 'CheckOnly created trace parent' }; " +
				"foreach($name in $names){if([Environment]::GetEnvironmentVariable($name,'Process') -cne $saved[$name]){throw ('CheckOnly changed environment: '+$name)}}; " +
				"$refused=$false; try { " + invocation + " -TraceFile " + psLiteral(checkTrace) + " | Out-Null } catch { if ($_.Exception.Message -notmatch 'existing parent folder') { throw }; $refused=$true }; " +
				"if (-not $refused) { throw 'Launch accepted missing trace parent' }; " +
				"if (Test-Path -LiteralPath " + psLiteral(proxyDir) + ") { throw 'Rejected launch created proxy' }; " +
				"$env:GIT_COW_TEST_HELPER='launcher'; $env:GIT_COW_TEST_REPORT=" + psLiteral(reportFile) + "; " +
				invocation + " -Trace -TraceFile " + psLiteral(filepath.Base(traceFile)) + " | Out-String; " +
				"foreach($name in $names){if([Environment]::GetEnvironmentVariable($name,'Process') -cne $saved[$name]){throw ('Environment not restored: '+$name)}}"
			// Each shell receives its own fresh shim folder and report file.
			localProxy := filepath.Join(base, shell+" shim Ω")
			localReport := filepath.Join(base, shell+" report.json")
			command = strings.ReplaceAll(command, psLiteral(proxyDir), psLiteral(localProxy))
			command = strings.ReplaceAll(command, psLiteral(reportFile), psLiteral(localReport))
			env := cleanEnv(t)
			env = withEnv(env, "GIT_COW_REAL_GIT", git)
			env = withEnv(env, "GIT_COW_SHIM_TRACE", "old trace")
			env = withEnv(env, traceFileEnv, filepath.Join(base, "caller trace.jsonl"))
			env = withEnv(env, "GIT_COW_SHIM_DEPTH", "4")
			env = withEnv(env, "GIT_COW_SHIM_BYPASS", "old bypass")
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
			cmd.Env = env
			cmd.Dir = base
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("launcher: %v\n%s", err, out)
			}
			deadline := time.Now().Add(10 * time.Second)
			var data []byte
			for time.Now().Before(deadline) {
				data, err = os.ReadFile(localReport)
				if err == nil && json.Valid(data) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil || !json.Valid(data) {
				t.Fatalf("launcher helper did not report: %v %q\n%s", err, data, out)
			}
			var report helperReport
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			wantArgs, _ := json.Marshal(args)
			gotArgs, _ := json.Marshal(report.Args)
			if !bytes.Equal(gotArgs, wantArgs) || report.Real != git || report.Depth != "" || report.Bypass != "" || report.TraceFile != traceFile {
				t.Fatalf("launcher report=%+v, want args=%q", report, args)
			}
			prefix := localProxy + ";" + filepath.Dir(cow) + ";"
			if !strings.HasPrefix(report.Path, prefix) {
				t.Fatalf("launcher child PATH %q lacks %q", report.Path, prefix)
			}
			proxyBytes, err := os.ReadFile(filepath.Join(localProxy, "git.exe"))
			if err != nil {
				t.Fatal(err)
			}
			shimBytes, _ := os.ReadFile(shimBinary)
			if !bytes.Equal(proxyBytes, shimBytes) {
				t.Fatal("launcher did not copy shim to dedicated git.exe")
			}
			entries, err := os.ReadDir(localProxy)
			if err != nil || len(entries) != 1 || entries[0].Name() != "git.exe" {
				t.Fatalf("unexpected shim directory contents: %v %v", entries, err)
			}
			if _, err := os.Stat(traceFile); !os.IsNotExist(err) {
				t.Fatalf("launcher created trace without a shim dispatch: %v", err)
			}
			// Keep the copied proxy running. Windows refuses to overwrite an
			// executable in use, but a matching proxy should be reusable.
			running := exec.Command(filepath.Join(localProxy, "git.exe"), "hash-object", "--stdin")
			running.Env = withEnv(cleanEnv(t), realGitEnv, git)
			input, err := running.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := running.Start(); err != nil {
				input.Close()
				t.Fatal(err)
			}
			defer func() {
				input.Close()
				if err := running.Wait(); err != nil {
					t.Errorf("running proxy: %v", err)
				}
			}()
			reuseInvocation := strings.ReplaceAll(invocation, psLiteral(proxyDir), psLiteral(localProxy))
			if err := os.Remove(localReport); err != nil {
				t.Fatal(err)
			}
			reuse := "$ErrorActionPreference='Stop'; $env:GIT_COW_TEST_HELPER='launcher'; $env:GIT_COW_TEST_REPORT=" + psLiteral(localReport) + "; " + reuseInvocation + " | Out-String"
			cmd = exec.Command(path, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", reuse)
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("reuse running matching proxy: %v\n%s", err, out)
			}
			deadline = time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				data, err = os.ReadFile(localReport)
				if err == nil && json.Valid(data) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil || json.Unmarshal(data, &report) != nil || report.TraceFile != "" {
				t.Fatalf("launcher did not clear inherited trace file without -TraceFile: %v %q", err, data)
			}
		})
	}
}

func TestT3LauncherRejectsUnrelatedProxy(t *testing.T) {
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable")
	}
	git := realGit(t)
	cow := cowBinary(t)
	script, _ := filepath.Abs(filepath.Join("..", "..", "integrations", "t3", "Start-T3WithCow.ps1"))
	self, _ := os.Executable()
	dir := t.TempDir()
	proxy := filepath.Join(dir, "git.exe")
	copyExecutable(t, git, proxy)
	before, _ := os.ReadFile(proxy)
	command := "$ErrorActionPreference='Stop'; try { & " + psLiteral(script) + " -RealGit " + psLiteral(git) + " -CowPath " + psLiteral(cow) + " -ShimPath " + psLiteral(shimBinary) + " -T3Path " + psLiteral(self) + " -ShimDirectory " + psLiteral(dir) + "; exit 2 } catch { if ($_.Exception.Message -notmatch 'differs from this shim') { throw }; exit 0 }"
	cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Env = cleanEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("expected refusal to overwrite unrelated Git: %v\n%s", err, out)
	}
	after, _ := os.ReadFile(proxy)
	if !bytes.Equal(before, after) {
		t.Fatal("launcher modified unrelated Git")
	}
}

func TestT3LauncherRestoresTraceFileAfterStartFailure(t *testing.T) {
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable")
	}
	base := t.TempDir()
	badT3 := filepath.Join(base, "invalid T3.exe")
	if err := os.WriteFile(badT3, []byte("invalid executable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	proxyDir := filepath.Join(base, "proxy")
	log := filepath.Join(base, "new trace.jsonl")
	script, _ := filepath.Abs(filepath.Join("..", "..", "integrations", "t3", "Start-T3WithCow.ps1"))
	command := "$ErrorActionPreference='Stop'; $saved=$env:GIT_COW_SHIM_TRACE_FILE; $failed=$false; try { & " + psLiteral(script) +
		" -RealGit " + psLiteral(realGit(t)) + " -CowPath " + psLiteral(cowBinary(t)) + " -ShimPath " + psLiteral(shimBinary) +
		" -T3Path " + psLiteral(badT3) + " -ShimDirectory " + psLiteral(proxyDir) + " -TraceFile " + psLiteral(log) +
		" | Out-Null } catch { $failed=$true }; if (-not $failed) { throw 'Invalid T3 unexpectedly launched' }; " +
		"if ($env:GIT_COW_SHIM_TRACE_FILE -cne $saved) { throw 'Trace file environment not restored after start failure' }; " +
		"if (-not (Test-Path -LiteralPath " + psLiteral(filepath.Join(proxyDir, "git.exe")) + ")) { throw 'Failure happened before process launch' }"
	cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Env = withEnv(cleanEnv(t), traceFileEnv, filepath.Join(base, "caller trace.jsonl"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launcher failed to restore trace environment: %v\n%s", err, out)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("failed launch created trace file: %v", err)
	}
}
