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

## Silent desktop shortcut

The same [windowless helper used for T3](../t3/HiddenLauncher.cs) can start this
launcher without the PowerShell terminal flash. Build it with the Windows .NET
Framework compiler as described in the [silent launch guide](../../docs/integrations.md#silent-launch-and-taskbar-pinning),
using `ChatGPTCowLauncher.exe` as the output filename. It runs Windows PowerShell
5.1 with `CreateNoWindow`; the Git shim and COW binaries need no changes.

For an installation at `C:\Tools\git-cow-worktree`, set the COW shortcut fields to:

| Shortcut field | Value |
| --- | --- |
| Target | `C:\Tools\git-cow-worktree\ChatGPTCowLauncher.exe` |
| Arguments | `"C:\Tools\git-cow-worktree\integrations\codex\Start-ChatGPTWithCowTrace.ps1"` |
| Start in | `C:\Tools\git-cow-worktree` |

Keep any existing `-RealGit`, `-CowPath`, `-ShimPath` and `-LogDirectory` arguments
after the `.ps1` path. A personal wrapper script can be used in its place, as in
the tested installation. To convert an existing PowerShell shortcut, replace its
target with the helper and retain the portion of its arguments after `-File`.
Back up that shortcut before editing it. Use `-CheckOnly` for a preflight without
launching or interrupting the running app.

## Icon matching

The normal packaged ChatGPT shortcut can resolve its icon through the Windows
package instead of an explicit `.ico` path. Its `IconLocation` can therefore be
empty. `ChatGPT.exe` has its own embedded icon; extracting that icon can produce
a different logo from the normal shortcut.

In the tested `26.1007.2314.0` installation, the normal shortcut's logo came from
the package's `assets/Square44x44Logo.targetsize-*_altform-unplated.png` images.
The previous COW shortcut used a separate `.ico` with the executable's logo.
The replacement packs the original package PNGs into a local `.ico` at sizes
from 16 to 256 pixels and sets the COW shortcut's icon to that file. The PNG data
is preserved. The normal shortcut stays unchanged, and no OpenAI icon assets are
bundled in this repository.

Keep the copied icon under your permanent COW installation directory. A local
copy preserves the chosen appearance across Windows package version-directory
changes. Inspect the installed package and its manifest when choosing an icon
on another version; resource filenames and artwork can change.

## Taskbar grouping

Windows [groups taskbar windows and shortcuts by application identity](https://learn.microsoft.com/en-us/windows/win32/shell/appids).
Changing the shortcut's name or deleting the normal desktop shortcut does not
correct an identity mismatch. A COW shortcut that targets the helper needs the
same `System.AppUserModel.ID` as the native app it launches.

For the tested direct launch of app `26.1007.2314.0`, the native ChatGPT Start
Menu shortcut carries `com.openai.codex`. The packaged desktop shortcut uses a
separate package activation route, so use the native Start Menu shortcut's ID
for this COW launcher. Inspect the installed version instead of deriving the ID
from the display name.

The [shortcut identity helper](../windows/Set-ShortcutAppId.ps1) changes only
that metadata. It keeps the COW target, script arguments, icon and working
directory. This example backs up and updates the existing COW desktop shortcut;
it does not pin anything:

```powershell
$ErrorActionPreference = 'Stop'
$installRoot = 'C:\Tools\git-cow-worktree'
$nativeMenu = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\ChatGPT.lnk'
$cowShortcut = Join-Path ([Environment]::GetFolderPath('Desktop')) 'ChatGPT COW trace.lnk'
$shell = New-Object -ComObject Shell.Application
$item = $shell.Namespace((Split-Path $nativeMenu -Parent)).ParseName((Split-Path $nativeMenu -Leaf))
$appId = $item.ExtendedProperty('System.AppUserModel.ID')
if (-not $appId) { throw 'Native ChatGPT shortcut has no explicit app ID; inspect this installation before setting one.' }
$backup = Join-Path $installRoot ('ChatGPT-shortcut-before-appid-{0}.lnk' -f [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss-fffffff'))
Copy-Item -LiteralPath $cowShortcut -Destination $backup
& (Join-Path $installRoot 'integrations\windows\Set-ShortcutAppId.ps1') `
    -ShortcutPath $cowShortcut -AppId $appId
```

If the old COW shortcut is already pinned, unpin that entry and pin the corrected
desktop shortcut through **Show more options → Pin to taskbar**. Windows can
cache the earlier pin's identity, so changing only the desktop copy may leave
the split visible until the pin is refreshed. Fully exit ChatGPT before testing
the next taskbar launch. Keep the normal shortcut for launching without COW.

The metadata helper was tested under PowerShell 5.1 and 7. Checks confirm that
the launch fields and icon survive the identity change, `-WhatIf` leaves the
file unchanged, and an invalid ID is rejected without changing the shortcut.
On the test machine, both existing COW shortcut copies were corrected. The user
confirmed correct taskbar grouping on October 13, 2026 after the app identity
change. The normal shortcuts and COW launch fields stayed unchanged.

## Verify worktree creation

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
