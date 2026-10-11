# ChatGPT desktop worktree tracing on Windows

This experimental launcher records the Git commands used by Codex inside the
ChatGPT desktop app. Automatic COW creation was verified on Windows with app
version `26.1007.2314.0` on October 11, 2026. The current shim optimizes supported
`worktree add` commands and forwards
`--no-checkout`, `reset --hard`, and other commands to ordinary Git.

The live test created a detached worktree from a clean `main` checkout on ReFS.
The app invoked `git [global options] worktree add --detach <path> refs/heads/main`;
the shim changed the subcommand to `cow-worktree` and returned exit code 0. All
tracked contents matched, Git status was clean, and all 64 MiB of fixture binary
data shared physical disk extents with the source. A normal-app baseline shared
0 bytes. This result covers that app version and creation flow; other managed
worktree implementations and handoff flows need their own tests.

The launcher discovers the installed `OpenAI.Codex` Windows package each time
it starts. Supply `-AppPath` for a different native installation. It uses a
dedicated, versioned Git proxy directory and changes only the new child process's
environment. It refuses to start another ChatGPT instance while the app is
already running.

```powershell
# Inspect paths without creating a proxy, logs, or app process.
& .\integrations\codex\Start-ChatGPTWithCowTrace.ps1 -CheckOnly

# Verify the proxy and trace writers without starting ChatGPT.
& .\integrations\codex\Start-ChatGPTWithCowTrace.ps1 -PrepareOnly

# Fully exit ChatGPT first, including its tray/background instance.
& .\integrations\codex\Start-ChatGPTWithCowTrace.ps1
```

Use `-RealGit`, `-CowPath`, and `-ShimPath` for separately installed binaries.
`-LogDirectory` chooses the trace parent. Each launch gets a new session folder
containing `shim.jsonl`, `git.log`, `git-events.jsonl`, and `launch.json`.
`latest.json` in the parent identifies the latest session. Every session starts
with a recorded `git --version` preflight; that record is a launcher check,
not evidence that ChatGPT has called the proxy. `-PassThrough` records shim
decisions while forwarding all Git commands without rewriting them.

Git's own traces can record commands launched through an absolute Git path,
provided the app passes the tracing environment to its Git processes. A missing
app command in the shim log alone cannot prove which executable was used. Trace
files record Git arguments; use the disposable fixture for this experiment.

For the fixture prepared by `integrations/t3/smoke/Prepare-SmokeRepo.ps1`:

1. Set ChatGPT **Settings > Worktrees > Worktree root** to a directory on the
   same ReFS volume as the fixture, such as `V:\worktrees\codex`.
2. Open `V:\cow-smoke\repo`, select **Codex > This computer > Worktree**, and
   start from `main`. Leave setup scripts disabled for this test.
3. Ask for the working directory, `git status --short`, and
   `git rev-parse --git-common-dir`. Leave tracked files unchanged.
4. Inspect the new session logs for the worktree path. Then run:

```powershell
& V:\cow-smoke\tools\Verify-Worktree.ps1 `
  -FixtureRoot V:\cow-smoke -WorktreePath '<directory reported by Codex>'
```

This baseline verifier checks contents and Git metadata, then reports physical
block sharing. To require both full sharing and a successful ChatGPT shim
dispatch, read `latest.json` from your launcher's log directory and run:

```powershell
$logDirectory = Join-Path $env:LOCALAPPDATA 'git-cow-worktree\chatgpt-traces'
$launch = Get-Content (Join-Path $logDirectory 'latest.json') -Raw | ConvertFrom-Json
& V:\cow-smoke\tools\Verify-Worktree.ps1 `
  -FixtureRoot V:\cow-smoke -WorktreePath '<directory reported by Codex>' `
  -TraceFile $launch.ShimTrace -RequireCow -RequireTrace
```

Set `$logDirectory` to your custom `-LogDirectory` if supplied. The fixture's
`Verify-Current.ps1` uses its T3-specific trace file, so use the direct verifier
with `-TraceFile` for ChatGPT tests. After creation passes, ask Codex to edit
only `hello.txt` and repeat with `-AllowEdits`. The source should stay unchanged
and the binary fixture data should continue sharing all 64 MiB.

The launcher leaves the normal ChatGPT shortcut and persistent PATH unchanged.
To stop using it, fully exit ChatGPT and launch the normal app shortcut.
