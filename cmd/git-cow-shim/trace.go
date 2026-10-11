package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const traceFileEnv = "GIT_COW_SHIM_TRACE_FILE"

type traceRecord struct {
	Event        string   `json:"event"`
	Timestamp    string   `json:"timestamp"`
	PID          int      `json:"pid"`
	Cwd          string   `json:"cwd"`
	Route        string   `json:"route"`
	RealGit      string   `json:"realGit"`
	Argv         []string `json:"argv"`
	OriginalArgv []string `json:"originalArgv"`
	Depth        int      `json:"depth"`
	ExitCode     *int     `json:"exitCode,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type fileTrace struct {
	path   string
	record traceRecord
}

func traceDiagnostic(err error) {
	fmt.Fprintf(os.Stderr, "git-cow-shim: %s: %v\n", traceFileEnv, err)
}

func newFileTrace(path, route, real string, argv, original []string, depth int) *fileTrace {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		traceDiagnostic(fmt.Errorf("must be an absolute file path"))
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		traceDiagnostic(err)
		return nil
	}
	return &fileTrace{
		path: filepath.Clean(path),
		record: traceRecord{
			PID: os.Getpid(), Cwd: cwd, Route: route, RealGit: real,
			Argv: append([]string{}, argv...), OriginalArgv: append([]string{}, original...),
			Depth: depth, // Incoming depth: the outermost shim records zero.
		},
	}
}

func (trace *fileTrace) write(event string, code *int, dispatchErr error) {
	if trace == nil || trace.path == "" {
		return
	}
	record := trace.record
	record.Event = event
	record.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	record.ExitCode = code
	if dispatchErr != nil {
		record.Error = dispatchErr.Error()
	}
	if err := appendTraceRecord(trace.path, record); err != nil {
		traceDiagnostic(err)
		trace.path = "" // Report only once; tracing must never affect Git's result.
	}
}

func appendTraceRecord(path string, record traceRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	// One append write per complete record. O_APPEND also uses FILE_APPEND_DATA
	// on Windows, so separate shim processes cannot overwrite each other's data.
	n, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return closeErr
}
