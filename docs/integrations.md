# AI coding tool integrations

`git cow-worktree add` creates a standard Git linked worktree. Any coding tool can open one created beforehand. Instructions can also ask an agent to use this command when it creates a worktree itself.

Applications can create worktrees before the agent starts. That requires an application hook or executable integration. T3 owns its worktree creation regardless of which provider it runs.

On Windows, source and destination must be on the same ReFS volume with block cloning support to share storage. A binary installed on NTFS can create ReFS worktrees. Creating the destination on NTFS, or on a different volume from the source, uses ordinary checkout.

## Claude Code

The core executable includes a native hook adapter. It reads Claude's JSON from stdin, sends Git output to stderr, and prints the new absolute directory path to stdout. No shell script or Git shim is needed.

Add the `hooks` entries from [the settings example](../integrations/claude/settings.json) to your existing Claude settings. Preserve other settings and hooks. Alternatively, try the example for one invocation:

```powershell
claude --settings 'C:\Tools\git-cow-worktree\integrations\claude\settings.json' --worktree feature-auth
```

`git-cow-worktree.exe` must be on the application's PATH. You can replace `command` in both entries with its absolute executable path. The `args` arrays use Claude's exec form, which preserves spaces and Unicode without shell quoting.

### Windows user settings and GUI

For all local projects, configure `%USERPROFILE%\.claude\settings.json`. Back up that file, then merge the example's `WorktreeCreate` and `WorktreeRemove` entries into its `hooks` object. Keep your other settings and hook groups. For one project, use that project's `.claude/settings.local.json` instead.

Set `command` in both entries to the installed executable's full path. For an archive extracted to `C:\Tools\git-cow-worktree`, use:

```json
"command": "C:\\Tools\\git-cow-worktree\\git-cow-worktree.exe"
```

For a per-user installation, use the resolved path under your user profile, such as `C:\Users\YourName\.local\bin\git-cow-worktree.exe`. Exec form does not expand `%USERPROFILE%` in `command`. Keep the create arguments as `["claude-hook", "create", "--copy-local"]` and the remove arguments as `["claude-hook", "remove"]`.

Open the repository in the Claude GUI and start a new local Code session that creates a worktree. The native hook handles creation automatically. You can use the normal Claude shortcut; the hook configuration needs no Git shim or launcher. A terminal session uses the same hooks with `claude --worktree <name>`. The adapter is built into `git-cow-worktree.exe`, so running it does not require Go.

### Creation and base branch

The default destination is `<main-checkout>/.claude/worktrees/<name>`, on branch `worktree-<name>`, including when Claude starts in a linked worktree. This keeps worktrees on the source volume. These hooks require a repository with a main checkout. Add `.claude/worktrees/` to the project's `.gitignore`, or add `/.claude/worktrees/` to the main checkout's `.git/info/exclude` for a local-only setup. Names must be single directory names, and the adapter rejects symlinked destination paths.

The adapter starts from cached `origin/HEAD` when that ref exists, falling back to local `HEAD`. It does not fetch. Set the create command's arguments to select the base explicitly:

```json
"args": ["claude-hook", "create", "--base-ref", "HEAD", "--copy-local"]
```

You can use another commit or branch for `--base-ref`. `HEAD` refers to the checkout where Claude started, and local files also come from that checkout. Claude's `worktree.baseRef` setting is not supplied to command hooks, so configure this adapter flag instead. Pull-request fetching and Claude's refresh of the remote default branch are not reproduced by the adapter.

To select an external worktree parent directory, add `"--root", "V:\\claude-worktrees\\my-project"`. Use a separate parent per repository. Keep that directory on the same supported volume as the source.

An ownership record lives in the linked worktree's Git metadata. Repeating creation for a recorded worktree returns its existing path without resetting edits. An existing directory without a matching record is refused. Failed creation rolls back the directory, Git registration, and unchanged new branch when ownership can be verified.

An interrupted create can leave a `.git-cow-<name>.lock` file in the worktree parent directory. If the creator is no longer running, remove that lock before retrying. The error reports its path.

### Local files and cleanup

The settings example enables `--copy-local`. This copies regular ignored files matched by `.worktreeinclude`, using Git's ignore-pattern matching. Tracked files and other untracked files are excluded. Existing target files are never overwritten, and symlinked files or directories are refused. Without that flag, the adapter copies only tracked files through checkout.

The remove hook accepts only a linked worktree recorded by this adapter in the same repository. It asks Git to remove the worktree and deletes the owned branch only if its tip still equals the initial commit. Branches with agent commits are retained. Dirty worktrees are retained by default; adding `"--force"` to the remove arguments explicitly permits Git to discard their local changes. Locked worktrees must be unlocked first.

When removal is refused, the directory remains and Claude may show the hook error only in its debug log. If cleanup retains a branch with commits, choose a new worktree name or manage that branch through Git before reusing the old name.

On Windows, another process holding the worktree open can prevent deletion after Git has already removed its registration. The branch remains. Close that process and inspect the leftover files before cleaning up the directory manually.

Claude's periodic sweep does not manage hook-created worktrees, so use its explicit removal flow or ordinary Git cleanup. Ordinary `git worktree remove` does not remove the adapter's branch.

The adapter uses normal Git checkout behavior, including your configured filter drivers and post-checkout hooks. Claude's built-in checkout suppresses repository-local filter drivers. A failing post-checkout hook runs once. For a direct `git cow-worktree add`, its completed worktree remains, matching Git's hook-failure behavior. A failed Claude create hook also rolls back that completed worktree because no path was handed to Claude.

The hook contract is integration-tested against real Git repositories on ReFS and NTFS. Direct tests of the configured hooks also verified that repeated creation preserves edits, dirty removal is refused, and clean removal deletes the directory and unchanged owned branch.

### Claude GUI verification on Windows

Automatic creation through the native hooks was verified in a Windows Claude GUI session on October 11, 2026. All 64 MiB of the ReFS fixture's binary files shared physical disk extents with the source. The ownership record, branch, HEAD, tracked contents and clean Git status matched; the source's tracked file hashes and HEAD stayed unchanged. The installed native Claude Code CLI was `2.1.296`; the GUI build version was not recorded. This test covers GUI worktree creation. GUI removal, pull-request worktree names and background-session lifecycle behavior still require separate live tests.

To repeat the creation check:

1. Prepare the [Windows smoke fixture](../integrations/t3/smoke/README.md#prepare-a-fixture) on ReFS. `-T3Path` is optional, and the fixture's T3 launcher is not needed for this test. Reuse an existing fixture or choose a new root; preparation refuses an existing directory. The following steps use `V:\cow-smoke`.
2. Add `/.claude/worktrees/` to `V:\cow-smoke\repo\.git\info\exclude`. Open `V:\cow-smoke\repo` in the Claude GUI and create a new local worktree session. Alternatively, run `claude --worktree claude-cow-test` from that source directory.
3. Send this first message, then keep the session open for verification:

   > Report the working directory. Run `git status --short` and `git rev-parse --git-common-dir`. Leave files unchanged.

4. From a separate PowerShell window, use the directory Claude reported:

   ```powershell
   $worktree = '<directory reported by Claude>'
   & V:\cow-smoke\tools\Verify-Worktree.ps1 `
     -FixtureRoot V:\cow-smoke -WorktreePath $worktree -RequireCow

   $realGit = 'C:\Program Files\Git\cmd\git.exe'
   $gitDir = (& $realGit -C $worktree rev-parse --absolute-git-dir).Trim()
   Get-Content -LiteralPath (Join-Path $gitDir 'git-cow-claude.json') -Raw |
     ConvertFrom-Json
   & $realGit -C V:\cow-smoke\repo status --short
   ```

The verifier requires correct Git metadata, original tracked contents, a clean target and all 64 MiB of binary data shared with the source. It writes a JSON report under the fixture's `logs` directory. The ownership record's `path`, `branch` and `initial_head` identify the worktree created by this adapter. The source status should also be clean.

Use `-RequireCow` for this native-hook check. `-RequireTrace` checks the Git shim and does not apply to this integration. A report with zero successful shim dispatches is expected when Claude calls the adapter directly.

For a subsequent edit check, ask Claude to edit only `hello.txt`, leave `assets/*.bin` unchanged, and repeat the verifier with `-AllowEdits`. The source should stay unchanged and all binary data should remain shared.

## OpenCode v2

The [OpenCode plugin](../integrations/opencode/README.md) registers a native worktree strategy through `ctx.worktree.transform`. It creates COW checkouts, uses standard Git removal, and reports only worktrees with its own ownership record. OpenCode manages inventory and session setup. Configure the plugin in `opencode.jsonc` and keep the worktree destination on the repository's ReFS volume. The integration targets OpenCode `2.0.26`; older v2 betas need their own compatibility check.

The actual OpenCode `2.0.26` server API was verified on Windows ReFS on October 11, 2026. All 64 MiB of fixture data shared physical extents, saved ownership survived refresh, dirty removal requested force confirmation, and clean removal deleted the checkout and unchanged branch. The source stayed unchanged.

Desktop GUI creation was verified on the same date. The `kind-harbor` worktree shared all 64 MiB with its ReFS source; its ownership record, branch, HEAD, tracked contents and clean Git status matched. The source's hashes and HEAD stayed unchanged. GUI removal and background-session lifecycle behavior still require separate live tests. Follow the plugin's [ReFS verification steps](../integrations/opencode/README.md#verify-on-refs) to repeat the creation check.

## Antigravity 2 on Windows

The experimental [Antigravity launcher](../integrations/antigravity/README.md) enables the existing Git shim for the Antigravity 2 desktop app. It discovers the installed executable through its desktop shortcut or a conventional installation path, checks Git trace writers, and limits environment changes to the launched process. It reuses the ChatGPT trace launcher with an explicit app executable.

Native New Worktree creation was verified with Antigravity `2.22.0` on October 11, 2026. Its initial NTFS destination shared 0 of 64 MiB with the ReFS source. After redirecting storage through a directory junction, native GUI creation shared all 64 MiB, with matching contents, correct HEAD and Git metadata, and clean status. All 11 source files and the source HEAD stayed unchanged. Isolated subagents, GUI cleanup and cancellation still need independent live tests. The [storage guide](../integrations/antigravity/README.md#keep-worktree-storage-on-refs) covers the junction, preservation of existing checkouts, desktop shortcuts and the extent check.

Live write isolation was verified in that worktree. Only byte 0 of one 16 MiB payload changed, from `0xF2` to `0xF3`, and exactly one 4 KiB cluster stopped sharing with the source. The other binary extents remained shared, and all 11 source files remained unchanged. The edit was left uncommitted.

## T3 Code on Windows

Windows archives contain `git-cow-shim.exe`. It is inactive under that name. The [T3 launcher](../integrations/t3/Start-T3WithCow.ps1) places a copy named `git.exe` in a dedicated shim directory and prepends that directory only to the launched process's PATH. It does not replace Git for Windows or change your saved PATH.

The proxy routes supported `git worktree add` calls through real Git's `cow-worktree` dispatch. Other Git commands go to real Git. Help requests, `--no-checkout`, `--orphan`, unsupported forms and explicit bypass calls use ordinary Git. If the COW executable is unavailable, it chooses ordinary Git before creating anything. It never retries ordinary creation after a COW failure.

The real Git executable can be selected with `GIT_COW_REAL_GIT`. `GIT_COW_SHIM_BYPASS=1` disables rewriting. `GIT_COW_SHIM_TRACE=1` reports dispatch decisions on stderr. `GIT_COW_SHIM_TRACE_FILE` appends UTF-8 JSONL start and exit records to an absolute file path. Its parent folder must already exist. Records include arguments, working directory, routing and exit status; an interrupted process can leave a start record without an exit record. A logging failure reports a diagnostic without changing Git's result.

The shim preserves process streams and exit status. Routed COW commands use a Windows Job Object to stop their child processes when the shim exits or is terminated. Ordinary Git commands keep normal process lifetimes, including background fsmonitor services.

Extract the Windows archive into a directory such as `C:\Tools\git-cow-worktree`. Check the paths before launching:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\integrations\t3\Start-T3WithCow.ps1 `
  -T3Path "$env:LOCALAPPDATA\Programs\T3 Code\T3 Code.exe" `
  -RealGit 'C:\Program Files\Git\cmd\git.exe' `
  -CheckOnly
```

Then use the same command without `-CheckOnly`, adding `-Trace` for the first test. Use `-TraceFile 'V:\cow-logs\shim.jsonl'` for durable evidence, after creating its parent directory. Supply `-CowPath` and `-ShimPath` if the two binaries are installed separately. Optional `-T3Arguments` accepts an argument array. Extra application arguments appended to a shortcut, including the updater's `--updated`, are forwarded to T3. `--check-only` is also accepted as a launcher preflight switch. `-ShimDirectory` defaults to a dedicated folder under local app data. For an updated shim, choose a fresh dedicated directory if the existing copy differs; the launcher refuses to overwrite unrelated executables.

### Suggested desktop shortcut

For everyday use, keep the extracted binaries and launcher in a permanent directory such as `C:\Tools\git-cow-worktree`. Point a shortcut at that launcher. The smoke fixture's launcher is for testing; it depends on the disposable fixture directory.

Run this in PowerShell to check the installation and create **T3 Code COW** on your desktop. Edit the installation, T3 and Git paths first. The T3 example below is the Nightly installation; use your actual executable path for another edition.

```powershell
$ErrorActionPreference = 'Stop'
$installRoot = 'C:\Tools\git-cow-worktree'
$t3Exe = Join-Path $env:LOCALAPPDATA 'Programs\t3code\T3 Code (Nightly).exe'
$realGit = 'C:\Program Files\Git\cmd\git.exe'
$launcher = Join-Path $installRoot 'integrations\t3\Start-T3WithCow.ps1'
$shimDirectory = Join-Path $env:LOCALAPPDATA 'git-cow-worktree\t3-shim'

& $launcher -T3Path $t3Exe -RealGit $realGit -ShimDirectory $shimDirectory -CheckOnly

$shortcutPath = Join-Path ([Environment]::GetFolderPath('Desktop')) 'T3 Code COW.lnk'
if (Test-Path -LiteralPath $shortcutPath) {
    throw "Shortcut already exists: $shortcutPath. Edit it in Properties or choose another name."
}
# Preserve T3's app identity, so its window and taskbar shortcut group together.
$installedShortcut = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\T3 Code (Nightly).lnk'
if (Test-Path -LiteralPath $installedShortcut -PathType Leaf) {
    Copy-Item -LiteralPath $installedShortcut -Destination $shortcutPath
}
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$shortcut.Arguments = '-NoProfile -ExecutionPolicy Bypass -File "{0}" -T3Path "{1}" -RealGit "{2}" -ShimDirectory "{3}"' -f $launcher, $t3Exe, $realGit, $shimDirectory
$shortcut.WorkingDirectory = $installRoot
$shortcut.IconLocation = "$t3Exe,0"
$shortcut.Description = 'Start T3 Code with COW worktree creation'
$shortcut.Save()
```

The snippet creates the shortcut without launching T3. If a launch fails, run the shortcut's target command in an existing PowerShell window to read the error. Exit T3 completely before opening the shortcut, so the new instance inherits the COW environment. A shortcut cannot change the environment of an already-running instance. For storage sharing, keep T3's worktree location on the source repository's ReFS volume.

For separately installed binaries, add `-CowPath` and `-ShimPath` to both the check command and the shortcut's arguments. You can edit those arguments later in the shortcut's **Properties → Target**. Desktop and taskbar entries created by T3's installer can still launch T3 directly, so use the **T3 Code COW** shortcut when enabling this integration.

### Silent launch and taskbar pinning

A shortcut that starts `powershell.exe` can briefly show a terminal before PowerShell applies `-WindowStyle Hidden`. The optional [windowless launcher](../integrations/t3/HiddenLauncher.cs) starts PowerShell with `CreateNoWindow` from a GUI executable. It runs the same COW script and forwards the remaining arguments, including the updater's `--updated`. This adds a small launcher executable; the COW and Git shim binaries need no changes.

Build the helper once with the Windows .NET Framework compiler, then update the desktop shortcut created above. This needs no Go. The example uses Windows PowerShell 5.1 and retains the script path and configuration already saved in the shortcut.

```powershell
$ErrorActionPreference = 'Stop'
$installRoot = 'C:\Tools\git-cow-worktree'
$source = Join-Path $installRoot 'integrations\t3\HiddenLauncher.cs'
$hiddenLauncher = Join-Path $installRoot 'T3CowLauncher.exe'
$shortcutPath = Join-Path ([Environment]::GetFolderPath('Desktop')) 'T3 Code COW.lnk'
$compiler = @(
    (Join-Path $env:SystemRoot 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'),
    (Join-Path $env:SystemRoot 'Microsoft.NET\Framework\v4.0.30319\csc.exe')
) | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
if (-not $compiler) { throw 'Windows .NET Framework compiler not found.' }
if (-not (Test-Path -LiteralPath $shortcutPath -PathType Leaf)) {
    throw 'Create the COW desktop shortcut first.'
}
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$fileArguments = [regex]::Match($shortcut.Arguments, '(?i)(?:^|\s)-File\s+(.+)$')
if ([IO.Path]::GetFileName($shortcut.TargetPath) -ine 'powershell.exe' -or -not $fileArguments.Success) {
    throw 'Expected a Windows PowerShell shortcut with -File arguments.'
}
& $compiler /nologo /target:winexe /reference:System.Windows.Forms.dll "/out:$hiddenLauncher" $source
if ($LASTEXITCODE -ne 0) { throw 'Windowless launcher compilation failed.' }
$backup = Join-Path $installRoot ('T3-shortcut-before-silent-{0}.lnk' -f [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss-fffffff'))
Copy-Item -LiteralPath $shortcutPath -Destination $backup
$shortcut.TargetPath = $hiddenLauncher
$shortcut.Arguments = $fileArguments.Groups[1].Value
$shortcut.Save()
```

The first argument to `T3CowLauncher.exe` is the absolute `.ps1` path; the rest are arguments for that script. Keep the script's `-T3Path`, `-RealGit`, and any other existing configuration. The helper preserves the working directory and child exit code. Its own startup errors appear in a dialog. For script diagnostics, run the original PowerShell command in an existing terminal. A personal wrapper can also show script errors in a dialog, as the tested installation does.

Give the installed Start Menu shortcut the same target and arguments when you want update launches to stay silent too. Preserve T3's icon, working directory and app identity by editing its existing `.lnk` or a copy of it. See [updates and restarts](#updates-and-restarts) for that separate update route.

To pin it manually, right-click the configured **T3 Code COW** shortcut, choose **Show more options → Pin to taskbar**. Pin this configured shortcut so taskbar launches use the COW environment. If it was pinned before changing the target, unpin that entry and pin the updated shortcut again. The setup snippets above do not pin anything automatically. The taskbar pin has no effect on T3's Start Menu update shortcut.

Silent desktop startup was confirmed on Windows on October 12, 2026. Tests cover the GUI executable, updater arguments, spaces, Unicode, quotes, trailing backslashes, empty arguments, literal shell characters, working directory and exit status. The installed launcher also passed `--check-only --updated`. The live update and ReFS sharing check below preceded the silent helper; a future update should still be checked after enabling it.

### Updates and restarts

Keep the COW installation outside T3's application directory. A T3 update should leave the launcher and companion binaries there intact. The shortcut remains usable as long as T3's executable stays at the configured path; edit its target if that path changes or you switch between Stable and Nightly.

[T3 updates can close and relaunch the desktop app](https://github.com/pingdotgg/t3code/blob/main/docs/user/updating.md). On Windows, its Electron updater starts a separate NSIS installer. The installer [opens the installed Start Menu shortcut](https://github.com/electron-userland/electron-builder/blob/master/packages/app-builder-lib/templates/nsis/installSection.nsh) when it exists and [passes `--updated`](https://github.com/electron-userland/electron-builder/blob/master/packages/app-builder-lib/templates/nsis/common.nsh). A desktop shortcut alone does not configure that update route. A launcher that accepts only its own flags can reject the extra argument.

To route automatic update launches through COW, back up the existing T3 Start Menu shortcut, then give it the same launcher target and arguments as your COW desktop shortcut. This can be PowerShell or the [windowless helper](#silent-launch-and-taskbar-pinning). For a current-user Nightly install, it is usually `%APPDATA%\Microsoft\Windows\Start Menu\Programs\T3 Code (Nightly).lnk`. Preserve its icon, working directory and app identity. If you use a personal wrapper script, it must collect remaining application arguments and pass them to `Start-T3WithCow.ps1` through `-T3Arguments`; the generic launcher already forwards arguments appended by the installer. Keep the backup to restore normal Start Menu launches later.

The updater argument and Windows shortcut launch route were tested with helper executables under PowerShell 5.1 and 7. A live Windows update from `0.0.46-nightly.20261011.2980` to `0.0.46-nightly.20261012.3021` was verified on October 12, 2026. The app reopened after updating and preserved the configured desktop and Start Menu shortcuts. A new worktree created through that GUI shared all 64 MiB of the ReFS fixture's binary data with its source. The trace recorded a successful COW dispatch, the new worktree had correct HEAD and contents with clean Git status, and all 11 source file hashes and the source HEAD stayed unchanged.

Repeat the check after later updates, since installer versions can change shortcut handling. Enable `-TraceFile`, apply the update, then create a new disposable worktree and run the [smoke check](../integrations/t3/smoke/README.md). If the trace does not show COW routing, exit T3 completely and reopen it through the COW shortcut.

A [normal T3 relaunch](https://github.com/pingdotgg/t3code/blob/main/apps/desktop/src/app/DesktopLifecycle.ts) starts its app executable with the existing arguments. That differs from an installer opening a shortcut. Each launch creates a new process; COW routing depends on the Git executable and environment it receives.

Existing COW worktrees retain their shared storage when the app restarts without the shim. The shim affects creation of new worktrees.

When updating `git-cow-worktree` itself, keep the launcher and binary paths consistent. If the new `git-cow-shim.exe` differs from the existing proxy, the launcher refuses to reuse that proxy. Choose a fresh dedicated `-ShimDirectory`, such as `...\git-cow-worktree\t3-shim-v0.0.2`, and update both the check command and the shortcut's target. Check the paths with `-CheckOnly`, then launch through the shortcut. A T3-only update does not require a new proxy directory.

To disable this integration, close the launched T3 instance and start T3 normally. The dedicated shim directory can be removed after the application exits. Saved PATH and Git installation need no restoration.

Before enabling it, set T3's **Settings → Storage → Worktree location** to a directory on the repository's ReFS volume, for example `V:\t3-worktrees`. The default under the user profile may be on NTFS.

Close T3 completely before launching it with the shim. T3 rebuilds PATH from PowerShell profiles and known tool directories, so verify the final application environment and create a disposable managed worktree with tracing enabled. A PowerShell profile that puts real Git first can defeat interception. A running desktop instance may retain its previous environment.

The tests exercise T3's `git -c checkout.workers=N worktree add` command shape, Git metadata, contents, fallback and Windows child cancellation. They do not certify every T3 desktop version's environment reconstruction. An upstream configurable worktree provisioner would remove the PATH dependency.

The [Windows smoke fixture](../integrations/t3/smoke/README.md) prepares a disposable ReFS repository and comparison worktrees, verifies actual shared extents and file isolation, and supplies a launcher and repeatable checks for a live T3 test with Codex as its provider.

To build the companion from source:

```powershell
go build -o git-cow-shim.exe ./cmd/git-cow-shim
```

The companion is packaged only in Windows releases. Go is needed to build from source, and is not needed to run either released binary.

## Cursor and Codex

For worktrees an agent creates through shell commands, add this guidance to project instructions:

> When you create a Git worktree yourself, use `git cow-worktree add` with the appropriate Git arguments, placing the destination on the same supported volume as the source. If the executable is unavailable, use ordinary `git worktree add`. Keep `--no-checkout` and `--orphan` workflows on ordinary Git. Use ordinary Git for other worktree subcommands.

Use Cursor project rules or `AGENTS.md`, and Codex's `AGENTS.md`. This guidance does not replace application-managed worktree provisioning.

Cursor's worktree setup commands run on the checkout it creates. Its Agents Window creation has no documented replacement hook, and shim compatibility remains unverified. Cursor cleanup can include externally created worktrees under its managed root, so choose their location deliberately.

Codex inside the Windows ChatGPT desktop app was verified with the optional shim on October 11, 2026, using app version `26.1007.2314.0`. The app issued `worktree add --detach` with global Git options, and the shim redirected it to `cow-worktree add`. All 64 MiB of the ReFS fixture's binary data shared physical disk extents with the source; contents, HEAD, and Git status were correct. The normal-app baseline shared 0 bytes. This test covers automatic creation from a clean `main` checkout in that app version.

Use the [ChatGPT worktree trace launcher](../integrations/codex/README.md) to enable the shim for the app and verify another installation. It records shim decisions and Git's own traces in separate files and checks the trace writers before starting the app. It resolves the installed Windows package at each launch, so package version directory changes do not require editing its path. The [silent desktop shortcut](../integrations/codex/README.md#silent-desktop-shortcut) reuses the windowless helper, the [icon guide](../integrations/codex/README.md#icon-matching) explains how to match the normal packaged app's logo, and the [taskbar guide](../integrations/codex/README.md#taskbar-grouping) covers app identity and refreshing an existing pin.

The previously inspected Codex Rust worktree implementation creates metadata with `worktree add --no-checkout`, then materializes files with `reset --hard`. The add-only shim preserves that workflow. It requires a separate provisioning integration if used by another Codex creation route; do not intercept `reset --hard` globally. Codex running as T3's provider uses the worktree provisioned by T3.

## Sources

- [Claude worktree hooks](https://code.claude.com/docs/en/hooks#worktreecreate) and [worktree behavior](https://code.claude.com/docs/en/worktrees).
- [T3 Git driver](https://github.com/pingdotgg/t3code/blob/main/apps/server/src/vcs/GitVcsDriverCore.ts), [Windows environment handling](https://github.com/pingdotgg/t3code/blob/main/apps/desktop/src/shell/DesktopShellEnvironment.ts), and [project settings](https://github.com/pingdotgg/t3code/blob/main/docs/user/project-settings.md).
- [Cursor worktrees](https://cursor.com/docs/configuration/worktrees) and [rules](https://cursor.com/docs/rules).
- [Codex worktree code](https://github.com/openai/codex/blob/main/codex-rs/worktree/src/lib.rs) and [external provisioner request](https://github.com/openai/codex/issues/50528).
