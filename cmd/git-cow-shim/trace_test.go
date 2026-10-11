package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func readTrace(t *testing.T, path string) []traceRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(data) || len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("trace must contain complete UTF-8 JSONL records: %q", data)
	}
	var records []traceRecord
	for _, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		var record traceRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("invalid JSONL: %v: %q", err, line)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"event", "timestamp", "pid", "cwd", "route", "realGit", "argv", "originalArgv", "depth"} {
			if _, ok := fields[key]; !ok {
				t.Fatalf("missing %s: %q", key, line)
			}
			delete(fields, key)
		}
		stamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil || !strings.HasSuffix(record.Timestamp, "Z") || stamp.IsZero() {
			t.Fatalf("invalid UTC timestamp %q: %v", record.Timestamp, err)
		}
		if record.PID <= 0 || !filepath.IsAbs(record.Cwd) || !filepath.IsAbs(record.RealGit) || record.Argv == nil || record.OriginalArgv == nil {
			t.Fatalf("invalid record: %+v", record)
		}
		switch record.Event {
		case "start":
			if len(fields) != 0 {
				t.Fatalf("unexpected start fields: %v", fields)
			}
		case "exit":
			if record.ExitCode == nil {
				t.Fatal("exit record has no exitCode")
			}
			delete(fields, "exitCode")
			delete(fields, "error")
			if len(fields) != 0 {
				t.Fatalf("unexpected exit fields: %v", fields)
			}
		default:
			t.Fatalf("unexpected event %q", record.Event)
		}
		records = append(records, record)
	}
	return records
}

func TestFileTraceDisabled(t *testing.T) {
	cwd := t.TempDir()
	for _, stderrTrace := range []string{"", "1"} {
		env := withEnv(helperEnv(t, "stdio"), "GIT_COW_SHIM_TRACE", stderrTrace)
		env = withEnv(env, traceFileEnv, "")
		_, stderr, code := capture(t, shimBinary, []string{"status"}, env, cwd, "")
		if code != 37 || strings.Contains(stderr, traceFileEnv) || strings.Contains(stderr, "route=") != (stderrTrace == "1") {
			t.Fatalf("disabled file trace: code=%d stderr=%q", code, stderr)
		}
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		t.Fatalf("disabled tracing created files: %v %v", entries, err)
	}
}

func TestFileTraceSchemaRoutingAndStreams(t *testing.T) {
	companion := t.TempDir()
	self, _ := os.Executable()
	copyExecutable(t, self, filepath.Join(companion, executableName("git-cow-worktree")))
	for _, item := range []struct {
		name, route  string
		args, routed []string
		depth        int
	}{
		{"ordinary", "ordinary command", []string{"status", "space 日本 Ω", `quotes" slash\`, "", "line\nnext"}, nil, 0},
		{"empty args", "ordinary command", []string{}, nil, 0},
		{"cow", "cow", []string{"-C", "space 日本", "worktree", "add", "target Ω", "HEAD"}, []string{"-C", "space 日本", "cow-worktree", "add", "target Ω", "HEAD"}, 0},
		{"nested", "bypass", []string{"worktree", "add", "space 日本", "HEAD"}, nil, 2},
	} {
		t.Run(item.name, func(t *testing.T) {
			cwd := t.TempDir()
			log := filepath.Join(cwd, "trace Ω.jsonl")
			env := withEnv(helperEnv(t, "stdio"), traceFileEnv, log)
			env = withEnv(env, "PATH", companion)
			env = withEnv(env, depthEnv, fmt.Sprint(item.depth))
			out, stderr, code := capture(t, shimBinary, item.args, env, cwd, "input Ω\n")
			if code != 37 || stderr != "helper stderr\n" {
				t.Fatalf("trace altered exit/stderr: code=%d stderr=%q stdout=%q", code, stderr, out)
			}
			var report helperReport
			if err := json.Unmarshal([]byte(out), &report); err != nil || report.Input != "input Ω\n" {
				t.Fatalf("trace altered streams: %v %+v", err, report)
			}
			records := readTrace(t, log)
			if len(records) != 2 || records[0].Event != "start" || records[1].Event != "exit" || *records[1].ExitCode != 37 || records[1].Error != "" || records[0].PID != records[1].PID {
				t.Fatalf("unexpected events: %+v", records)
			}
			routed := item.routed
			if routed == nil {
				routed = item.args
			}
			for _, record := range records {
				if !sameTestPath(t, record.Cwd, cwd) || record.RealGit != self || record.Route != item.route || record.Depth != item.depth || !reflect.DeepEqual(record.Argv, routed) || !reflect.DeepEqual(record.OriginalArgv, item.args) {
					t.Fatalf("incorrect dispatch record: %+v", record)
				}
			}
		})
	}
}

func TestFileTraceStartPrecedesDispatch(t *testing.T) {
	log := filepath.Join(t.TempDir(), "trace.jsonl")
	env := withEnv(helperEnv(t, "trace-observe"), traceFileEnv, log)
	out, stderr, code := capture(t, shimBinary, []string{"status"}, env, "", "")
	var observed traceRecord
	if err := json.Unmarshal([]byte(out), &observed); err != nil || observed.Event != "start" || code != 0 || stderr != "" {
		t.Fatalf("child did not observe exactly one start record: %v code=%d stderr=%q stdout=%q", err, code, stderr, out)
	}
	records := readTrace(t, log)
	if len(records) != 2 || records[1].Event != "exit" || *records[1].ExitCode != 0 {
		t.Fatalf("missing successful exit: %+v", records)
	}
}

func TestFileTraceBadPathsFailOpen(t *testing.T) {
	base := t.TempDir()
	missingParent := filepath.Join(base, "missing parent")
	for _, path := range []string{"relative.jsonl", base, filepath.Join(missingParent, "trace.jsonl")} {
		for _, mode := range []string{"cow-report", "stdio"} {
			env := withEnv(helperEnv(t, mode), traceFileEnv, path)
			out, stderr, code := capture(t, shimBinary, []string{"status"}, env, base, "input")
			wantCode := 0
			if mode == "stdio" {
				wantCode = 37
			}
			if code != wantCode || !json.Valid([]byte(out)) || strings.Count(stderr, traceFileEnv) != 1 || !strings.HasSuffix(stderr, "helper stderr\n") {
				t.Fatalf("bad path %q affected dispatch: code=%d stderr=%q stdout=%q", path, code, stderr, out)
			}
		}
	}
	for _, path := range []string{filepath.Join(base, "relative.jsonl"), missingParent} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("bad tracing path was created: %s: %v", path, err)
		}
	}
}

func TestFileTraceDispatchFailure(t *testing.T) {
	base := t.TempDir()
	badGit := filepath.Join(base, executableName("bad-git"))
	if err := os.WriteFile(badGit, []byte("invalid executable\n"), 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(base, "trace.jsonl")
	env := withEnv(cleanEnv(t), realGitEnv, badGit)
	env = withEnv(env, traceFileEnv, log)
	out, stderr, code := capture(t, shimBinary, []string{"status"}, env, base, "")
	if code != 1 || out != "" || stderr == "" {
		t.Fatalf("dispatch failure: %d %q %q", code, out, stderr)
	}
	records := readTrace(t, log)
	if len(records) != 2 || *records[1].ExitCode != code || records[1].Error == "" || !strings.Contains(stderr, records[1].Error) {
		t.Fatalf("dispatch failure not recorded: %+v, stderr=%q", records, stderr)
	}
}

func TestFileTraceConcurrentProcessesAppend(t *testing.T) {
	log := filepath.Join(t.TempDir(), "shared trace 日本.jsonl")
	env := withEnv(helperEnv(t, "cow-report"), traceFileEnv, log)
	// An existing complete pair must survive later concurrent appenders.
	_, _, code := capture(t, shimBinary, []string{"status", "seed"}, env, "", "")
	if code != 0 {
		t.Fatalf("seed exit=%d", code)
	}
	const count = 24
	var group sync.WaitGroup
	gate := make(chan struct{})
	errors := make([]error, count)
	for i := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			<-gate
			cmd := exec.Command(shimBinary, "status", fmt.Sprintf("process %d 日本 Ω", i))
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				errors[i] = fmt.Errorf("%w: %s", err, out)
			}
		}()
	}
	close(gate)
	group.Wait()
	for _, err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	records := readTrace(t, log)
	if len(records) != 2*(count+1) {
		t.Fatalf("lost records: got %d", len(records))
	}
	pairs := make(map[int][]traceRecord)
	arguments := make(map[string]bool)
	for _, record := range records {
		pairs[record.PID] = append(pairs[record.PID], record)
	}
	if len(pairs) != count+1 {
		t.Fatalf("got %d process pairs", len(pairs))
	}
	for pid, pair := range pairs {
		if len(pair) != 2 || pair[0].Event != "start" || pair[1].Event != "exit" || *pair[1].ExitCode != 0 || !reflect.DeepEqual(pair[0].Argv, pair[1].Argv) || !reflect.DeepEqual(pair[0].Argv, pair[0].OriginalArgv) {
			t.Fatalf("corrupt pair for pid %d: %+v", pid, pair)
		}
		arguments[pair[0].Argv[1]] = true
	}
	if !arguments["seed"] {
		t.Fatal("existing records were overwritten")
	}
	for i := range count {
		if !arguments[fmt.Sprintf("process %d 日本 Ω", i)] {
			t.Fatalf("missing process %d", i)
		}
	}
}
