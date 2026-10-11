// git-cow-shim is an optional native Git proxy. Install a copy named git.exe in
// an application-specific PATH directory; never replace the real Git binary.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var version = "dev"

const depthEnv = "GIT_COW_SHIM_DEPTH"
const maxDepth = 8

func main() {
	os.Exit(run(os.Args[0], os.Args[1:], os.Environ()))
}

func run(name string, args, env []string) int {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe")
	if base == "git-cow-shim" && len(args) == 1 && args[0] == "--version" {
		fmt.Printf("git-cow-shim %s\n", version)
		return 0
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "git-cow-shim:", err)
		return 1
	}
	depth := 0
	if value := envValue(env, depthEnv); value != "" {
		var err error
		depth, err = strconv.Atoi(value)
		if err != nil || depth < 0 || depth >= maxDepth {
			return fail(fmt.Errorf("invalid or excessive %s %q; refusing recursive Git execution", depthEnv, value))
		}
	}
	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	real, err := resolveRealGit(self, envValue(env, realGitEnv), envValue(env, "PATH"))
	if err != nil {
		return fail(err)
	}
	routed, reason := rewrite(args, depth > 0 || envValue(env, "GIT_COW_SHIM_BYPASS") != "")
	if reason == "cow" {
		if cow := resolveCow(self, envValue(env, "PATH")); cow != "" {
			// Git finds external commands using PATH. Prefer the chosen companion
			// directory, while preserving every other inherited environment entry.
			env = withEnv(env, "PATH", filepath.Dir(cow)+string(os.PathListSeparator)+envValue(env, "PATH"))
		} else {
			routed, reason = args, "cow unavailable"
		}
	}
	env = withEnv(env, realGitEnv, real)
	env = withEnv(env, depthEnv, strconv.Itoa(depth+1))
	env = withEnv(env, "GIT_COW_SHIM_BYPASS", "1")
	if envValue(env, "GIT_COW_SHIM_TRACE") == "1" {
		fmt.Fprintf(os.Stderr, "git-cow-shim: route=%s real=%q argv=%q\n", reason, real, routed)
	}
	trace := newFileTrace(envValue(env, traceFileEnv), reason, real, routed, args, depth)
	trace.write("start", nil, nil)
	code, err := runProcess(real, routed, env, reason == "cow")
	if err != nil {
		code = 1 // Match fail's effective returned status, including dispatch failures.
	}
	trace.write("exit", &code, err)
	if err != nil {
		return fail(err)
	}
	return code
}
