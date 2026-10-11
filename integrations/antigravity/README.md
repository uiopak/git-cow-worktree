# Antigravity 2 on Windows

This experimental launcher enables the existing Git shim for the
Antigravity 2 desktop app. It records shim routing
and Git's own commands so that native worktree creation can be tested.

Launcher discovery and Git trace preflight were checked with Antigravity
`2.22.0` on October 11, 2026. The app's native New
Worktree command was intercepted successfully, with correct contents and a clean
checkout. Its default destination was on NTFS and shared 0 of 64 MiB with the
ReFS source. After redirecting storage with a directory junction, the app's
native `check_git_environment_status` worktree shared all 64 MiB with the
source. Its HEAD, Git common directory, tracked contents and clean status
matched; all 11 source files and the source HEAD stayed unchanged. Isolated
subagents, GUI cleanup and cancellation still need separate live
tests. A clean `git status` alone does not establish COW sharing.

The live write-isolation test also passed. A streamed comparison of the entire
16 MiB `assets/payload-01.bin` found exactly one changed byte, at offset 0:
`0xF2` in the source and `0xF3` in the worktree. All 11 source files stayed
unchanged. One 4 KiB cluster stopped sharing with the source; the remaining
64 MiB minus 4 KiB of binary data stayed shared, including all three unedited
payload files. The test edit was left uncommitted.

The [documented hooks](https://antigravity.google/docs/hooks) operate around agent
tools and invocations. They do not document a worktree creation replacement.
The app's [New Worktree mode](https://antigravity.google/docs/projects/#worktree-selection-local-vs-new-worktree)
and an isolated subagent's [`branch` workspace](https://antigravity.google/docs/subagents/#invoking-subagents)
are separate creation paths and need separate live tests.

## Prepare and launch

Extract a Windows release archive, then run from its root:

```powershell
# Discover the desktop app and inspect paths without creating files or processes.
& .\integrations\antigravity\Start-AntigravityWithCowTrace.ps1 -CheckOnly

# Prepare the Git proxy and verify all trace writers without starting an app.
& .\integrations\antigravity\Start-AntigravityWithCowTrace.ps1 -PrepareOnly

# Finish your sessions and exit the selected app completely before launching it.
& .\integrations\antigravity\Start-AntigravityWithCowTrace.ps1
```

`-AppPath` accepts an explicit native executable. Otherwise, the launcher
resolves the original desktop shortcut or a conventional installation path at
each launch. `-ShortcutPath` selects another shortcut. Shortcuts with arguments
require explicit `-AppPath` and `-AppArguments` to preserve the intended argv.

Use `-RealGit`, `-CowPath`, and `-ShimPath` when the executables are installed
separately. `-AppArguments` forwards argument strings without shell evaluation.
`-LogDirectory` selects a trace parent, defaulting to
`%LOCALAPPDATA%\git-cow-worktree\antigravity-traces`.

Each launch writes `launch.json`, `shim.jsonl`, `git.log`, and `git-events.jsonl`
in a new session directory. `latest.json` in the parent points to that session.
Trace files contain command arguments and paths; keep them local when they
include private repositories. The initial `git --version` records are preflight,
not evidence of an Antigravity worktree command.

The launcher reuses the adjacent [Git trace launcher](../codex/Start-ChatGPTWithCowTrace.ps1)
with an explicit executable, so keep both integration directories installed.
Only the new app's environment receives the proxy PATH. Existing app and language
server processes are not terminated. A running instance must be exited before
launching, since a new window could reuse its old environment.

## Desktop shortcuts and updates

Create a separate shortcut for COW launches. Set its target to
`%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe` and its arguments
to the corresponding installed launcher:

```text
-NoProfile -ExecutionPolicy Bypass -File "C:\Tools\git-cow-worktree\integrations\antigravity\Start-AntigravityWithCowTrace.ps1"
```

Keep the original shortcut for ordinary launches. Use a name such as
`Antigravity (COW test)`. Install the scripts in
a permanent location. After an app update or automatic restart, exit it fully
and reopen through its COW shortcut so the new backend inherits the environment.
An updated Git shim uses a new hash-named directory instead of overwriting a
running `git.exe` proxy.

## Keep worktree storage on ReFS

The tested app stored worktrees under
`%USERPROFILE%\.gemini\antigravity\worktrees`. If this folder is physically on
NTFS, a source on ReFS cannot share blocks with it. A directory junction can
redirect just this worktree folder to the source's ReFS volume while preserving
the paths used by existing conversations and Git metadata.

Exit the app and its backend first. For an existing folder, copy all contents,
including hidden files, to an empty ReFS destination. Verify file hashes and
retain the original folder under a dated backup name beside it. After the
original path is vacant, create the junction:

```powershell
$logical = Join-Path $env:USERPROFILE '.gemini\antigravity\worktrees'
$physical = 'V:\worktrees\antigravity'
# The destination must contain the verified copy, or be empty for a fresh setup.
if (-not (Test-Path -LiteralPath $physical)) {
  New-Item -ItemType Directory -Path $physical | Out-Null
}
# This fails if the original folder is still present.
New-Item -ItemType Junction -Path $logical -Target $physical
Get-Item -LiteralPath $logical | Select-Object FullName, LinkType, Target
```

Copied existing checkouts retain their contents and Git identity, but the copy
does not retroactively share blocks with the source. Test a new conversation.
An agent may report either the logical `C:` path or the physical `V:` path;
the extent check establishes where its files actually live. The verifier accepts
junctions and still rejects the main checkout or a worktree subdirectory.

The junction persists through app updates and ordinary launches. COW routing
still requires the COW shortcut. Keep the ReFS volume mounted at the same path.
To restore the original storage, exit the app, preserve changes made on ReFS,
remove only the junction itself and rename the retained backup to `worktrees`.
Keep the ReFS directory until its newer data has been accounted for.

## Test native worktree creation

1. Use the existing [ReFS smoke fixture](../t3/smoke/README.md#prepare-a-fixture),
   for example `V:\cow-smoke\repo`. It contains 64 MiB of dense binary data and
   its verifier can measure physical sharing. The T3 launcher is not needed.
2. Start Antigravity through its COW launcher. Add the fixture as a project,
   then create a conversation using **New Worktree** mode.
3. Put the destination on the source's ReFS volume, for example
   `V:\worktrees\antigravity`, using a destination setting or the junction
   setup above. A logical `C:` path can refer to ReFS through a junction. The
   launcher itself does not relocate app data or existing worktrees.
4. Send this first prompt and keep the worktree open for inspection:

   > Report the working directory. Run `git status --short`,
   > `git rev-parse --git-common-dir`, and `where.exe git`. Leave files unchanged.
   > Do not create a worktree yourself.

5. From another PowerShell window, inspect the latest session and verify the
   path the agent reported:

   ```powershell
   $logs = Join-Path $env:LOCALAPPDATA 'git-cow-worktree\antigravity-traces'
   $launch = Get-Content -LiteralPath (Join-Path $logs 'latest.json') -Raw | ConvertFrom-Json
   $worktree = '<directory reported by Antigravity>'
   & V:\cow-smoke\tools\Verify-Worktree.ps1 `
     -FixtureRoot V:\cow-smoke -WorktreePath $worktree `
     -TraceFile $launch.ShimTrace -RequireCow -RequireTrace
   ```

`route=cow` with exit code 0 establishes command routing. All 64 MiB sharing
physical extents, matching contents and correct Git metadata establish a COW
checkout. A destination on NTFS or another volume can route correctly and still
fall back to ordinary checkout.

If creation uses `--no-checkout` followed by a separate materialization command,
the shim deliberately passes that workflow through. If no creation call appears
in the shim log, check `git.log` and `git-events.jsonl` for an absolute Git
executable or a different checkout sequence. `-PassThrough` enables a baseline
trace session that forwards every Git command without rewriting. Do not
intercept unrelated `reset --hard` commands to compensate.

After the conversation test, request an isolated `branch` workspace subagent
and repeat the inspection while its worktree still exists. Automatic cleanup can
remove it before the physical extent check. Check cancellation and cleanup as
separate lifecycle tests; do not infer them from successful creation.

To check write isolation in the disposable fixture, ask the agent to XOR the
first byte of `assets/payload-01.bin` with `1`, changing only that file and
leaving the edit uncommitted. Older fixtures have an `AGENTS.md` rule that says
to leave binary files unchanged. Explicitly state that this is the binary
write-isolation test and authorize an exception to that rule for this one byte.
The fixture generator now allows an explicitly requested binary isolation test.
Keep an existing fixture's tracked instructions unchanged to preserve its
baseline. Record the source file's hash before and after.
The source must remain unchanged; the edited worktree file must differ. Inspect
extents again to see which clusters became private. A small text-file edit
alone does not check isolation of the shared binary data. Do not use
`-RequireCow` for the edited checkout: a write intentionally breaks some sharing.

If native creation bypasses the shim, use an explicitly created COW worktree in
Local mode while investigating that route:

```powershell
git -C V:\cow-smoke\repo cow-worktree add -b antigravity-cow-test V:\worktrees\antigravity\manual main
```

Open that directory directly in the app. This tests operation inside a
pre-created COW checkout; it does not establish automatic native provisioning.
