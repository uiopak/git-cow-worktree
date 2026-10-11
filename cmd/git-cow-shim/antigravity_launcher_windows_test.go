//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAntigravityTraceLauncher(t *testing.T) {
	git, cow := realGit(t), cowBinary(t)
	script, err := filepath.Abs(filepath.Join("..", "..", "integrations", "antigravity", "Start-AntigravityWithCowTrace.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	appBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"space 日本", `quote"`, `V:\path with space\`, "", "literal $(never-run) `ticks` & ;"}
	var literals []string
	for _, arg := range args {
		literals = append(literals, psLiteral(arg))
	}
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		for _, application := range []string{"App", "IDE"} {
			t.Run(shell+"/"+application, func(t *testing.T) {
				shellPath, err := exec.LookPath(shell)
				if err != nil {
					t.Skipf("%s unavailable", shell)
				}
				base := t.TempDir()
				app := filepath.Join(base, "Antigravity probe Ω.exe")
				if err := os.WriteFile(app, appBytes, 0755); err != nil {
					t.Fatal(err)
				}
				proxyDir, logDir := filepath.Join(base, "proxy Ω"), filepath.Join(base, "logs 日本")
				reportPath, launchPath := filepath.Join(base, "child.json"), filepath.Join(base, "launch.json")
				invocation := "& " + psLiteral(script) + " -Application " + application + " -AppPath " + psLiteral(app) +
					" -RealGit " + psLiteral(git) + " -CowPath " + psLiteral(cow) + " -ShimPath " + psLiteral(shimBinary) +
					" -ShimDirectory " + psLiteral(proxyDir) + " -LogDirectory " + psLiteral(logDir) +
					" -AppArguments @(" + strings.Join(literals, ",") + ")"
				passThrough := application == "IDE"
				if passThrough {
					invocation += " -PassThrough"
				}
				command := "$ErrorActionPreference='Stop'; " +
					"$names=@('PATH','GIT_COW_REAL_GIT','GIT_COW_SHIM_BYPASS','GIT_COW_SHIM_DEPTH','GIT_COW_SHIM_TRACE_FILE','GIT_TRACE','GIT_TRACE2_EVENT','ELECTRON_RUN_AS_NODE'); $saved=@{}; " +
					"foreach($name in $names){$saved[$name]=[Environment]::GetEnvironmentVariable($name,'Process')}; " +
					"$check=" + invocation + " -CheckOnly; " +
					"if($check.App -ine " + psLiteral(app) + "){throw 'App resolution failed'}; " +
					"if((Test-Path -LiteralPath " + psLiteral(proxyDir) + ") -or (Test-Path -LiteralPath " + psLiteral(logDir) + ")){throw 'CheckOnly created files'}; " +
					"$prepared=" + invocation + " -PrepareOnly; " +
					"if($prepared.State -ne 'prepared' -or $prepared.AppProcessId){throw 'PrepareOnly started an app'}; " +
					"$launch=" + invocation + "; $launch | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath " + psLiteral(launchPath) + " -Encoding UTF8; " +
					"foreach($name in $names){if([Environment]::GetEnvironmentVariable($name,'Process') -cne $saved[$name]){throw ('Parent environment changed: '+$name)}}"
				env := cleanEnv(t)
				// A PowerShell 7 host's module path is not a PowerShell 5.1 module
				// path. Let each child shell establish its own standard modules.
				env = withEnv(env, "PSModulePath", "")
				env = withEnv(env, "GIT_COW_TEST_HELPER", "launcher")
				env = withEnv(env, "GIT_COW_TEST_REPORT", reportPath)
				env = withEnv(env, "GIT_COW_SHIM_BYPASS", "caller bypass")
				env = withEnv(env, "GIT_COW_SHIM_DEPTH", "4")
				env = withEnv(env, "ELECTRON_RUN_AS_NODE", "1")
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, shellPath, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
				cmd.Env, cmd.Dir = env, base
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("launcher: %v\n%s", err, out)
				}
				var launch struct {
					App, Proxy, Session, ShimTrace, GitTrace, GitEvents, State string
					AppProcessID                                               int
				}
				data, err := os.ReadFile(launchPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), &launch); err != nil {
					t.Fatal(err)
				}
				if launch.State != "launched" || launch.AppProcessID == 0 || launch.App != app || launch.Proxy != filepath.Join(proxyDir, "git.exe") {
					t.Fatalf("invalid launch report: %+v", launch)
				}
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					data, err = os.ReadFile(reportPath)
					if err == nil && json.Valid(data) {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				var report helperReport
				if err != nil || json.Unmarshal(data, &report) != nil {
					t.Fatalf("child did not report: %v %q", err, data)
				}
				wantBypass := ""
				if passThrough {
					wantBypass = "1"
				}
				if !reflect.DeepEqual(report.Args, args) || report.Real != git || report.Bypass != wantBypass || report.Depth != "" || report.TraceFile != launch.ShimTrace || report.Cwd != base {
					t.Fatalf("child environment/args differ: %+v", report)
				}
				if !strings.HasPrefix(report.Path, proxyDir+";"+filepath.Dir(cow)+";") {
					t.Fatalf("child PATH missing proxy: %s", report.Path)
				}
				for _, trace := range []string{launch.ShimTrace, launch.GitTrace, launch.GitEvents} {
					info, err := os.Stat(trace)
					if err != nil || info.Size() == 0 {
						t.Fatalf("missing trace preflight: %s: %v", trace, err)
					}
				}
				records := readTrace(t, launch.ShimTrace)
				if len(records) != 2 || records[0].Event != "start" || records[1].Event != "exit" || records[1].ExitCode == nil || *records[1].ExitCode != 0 {
					t.Fatalf("invalid preflight trace: %+v", records)
				}
			})
		}
	}
}

func TestAntigravityTraceLauncherRefusesRunningApp(t *testing.T) {
	self, _ := os.Executable()
	app := filepath.Join(t.TempDir(), "Antigravity running probe.exe")
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, data, 0755); err != nil {
		t.Fatal(err)
	}
	// Hold only this copied test helper open through stdin. No live application
	// or existing user process is started or terminated by the test.
	helper := exec.Command(app)
	helper.Env = withEnv(cleanEnv(t), "GIT_COW_TEST_HELPER", "stdio")
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); helper.Wait() }()
	script, _ := filepath.Abs(filepath.Join("..", "..", "integrations", "antigravity", "Start-AntigravityWithCowTrace.ps1"))
	base := t.TempDir()
	proxy, logs := filepath.Join(base, "proxy"), filepath.Join(base, "logs")
	command := "& " + psLiteral(script) + " -AppPath " + psLiteral(app) + " -RealGit " + psLiteral(realGit(t)) +
		" -CowPath " + psLiteral(cowBinary(t)) + " -ShimPath " + psLiteral(shimBinary) + " -ShimDirectory " + psLiteral(proxy) + " -LogDirectory " + psLiteral(logs)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Env = withEnv(cleanEnv(t), "PSModulePath", "")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "is already running") {
		t.Fatalf("did not refuse existing app: %v\n%s", err, out)
	}
	for _, path := range []string{proxy, logs} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected launch created %s: %v", path, err)
		}
	}
}
