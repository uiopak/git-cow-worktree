# OpenCode v2 COW worktrees

This plugin registers `git-cow-worktree` as OpenCode v2's native worktree strategy.
It uses the [worktree plugin API](https://opencode.ai/v2/docs/build/plugins#worktrees),
so OpenCode owns session setup and inventory while this plugin creates the checkout.
The plugin targets OpenCode `2.0.26` and its matching `@opencode/plugin` SDK.
OpenCode v1 and older v2 betas are not covered by this integration.

On October 11, 2026, OpenCode `2.0.26`'s actual worktree API loaded the plugin,
created a clean ReFS checkout sharing all 64 MiB of the fixture's binary data,
and recorded `git-cow-worktree` as its owner. Refresh preserved that owner without
adopting unrelated worktrees. Dirty removal requested force confirmation; clean
removal removed the restored checkout and its unchanged branch. Source hashes
and Git status stayed unchanged.

A desktop GUI-created session was also verified on October 11, 2026. Its
`kind-harbor` worktree shared all 64 MiB with the ReFS source and had the plugin's
matching ownership record, branch, HEAD and tracked contents. Both checkouts
were clean and the source hashes stayed unchanged. GUI removal and background
session lifecycle behavior still need separate live tests.

## Install and configure

Install `git-cow-worktree` and Git first. Keep this plugin directory in a permanent
location, then install its dependencies with Bun:

```powershell
cd C:\Tools\git-cow-worktree\integrations\opencode
bun install --production --frozen-lockfile
```

Merge this entry into the `plugins` array in your global
`%USERPROFILE%\.config\opencode\opencode.jsonc`, or in a project's `opencode.jsonc`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "worktree": { "directory": "V:\\worktrees\\opencode" },
  "plugins": [
    {
      "package": "C:/Tools/git-cow-worktree/integrations/opencode",
      "options": {
        "executable": "C:\\Tools\\git-cow-worktree\\git-cow-worktree.exe",
        "gitExecutable": "C:\\Program Files\\Git\\cmd\\git.exe"
      }
    }
  ]
}
```

Adjust the paths and preserve existing settings and plugins. Each executable
option accepts an absolute path or a command name on PATH. The defaults are
`git-cow-worktree` and `git`. Relative executable paths are refused.

Source and destination must be on the same supported volume for sharing. Use
the repository's ReFS volume on Windows. NTFS and cross-volume creation fall
back to ordinary checkout through the COW executable.

Open a new OpenCode session after changing the configuration. If a background
server is already running, restart it when your sessions have finished. OpenCode
selects the last registered strategy; avoid configuring another worktree
strategy after this plugin. A normal desktop shortcut can be used.

## Creation and cleanup

The plugin honors OpenCode's suggested destination. The optional `branch` input
is a starting ref, not a new branch name. It resolves that ref in the requested
source checkout and creates a unique `opencode/cow-<UUID>` branch. The default is
that source checkout's `HEAD`. Existing destinations are refused.

Only worktrees with a matching `git-cow-opencode.json` record in their linked Git
metadata are reported as plugin-owned. Ordinary Git and Claude worktrees are
excluded. The record identifies the directory, repository, branch and initial
commit. Removal validates it before calling Git.

Dirty removal requests OpenCode's force confirmation. A force request discards
the worktree's local changes. Branches with commits after creation are retained;
an unchanged owned branch is deleted with a compare-and-delete operation.
Git locks continue to protect locked worktrees. Changing a worktree to another
branch requires ordinary Git cleanup because its ownership record no longer
matches.

Subprocesses use argument vectors and support spaces and Unicode without shell
quoting. Cancellation stops their process group on Unix or uses Windows
`taskkill /T` to stop the process tree. Failed creation attempts to remove its
own checkout and unchanged branch. Changes made by a failing checkout hook are
retained for inspection if ordinary Git removal refuses them.

OpenCode keeps the strategy that created a worktree as its owner. Keep this
plugin installed until its worktrees are removed. To disable new COW creation,
remove the plugin entry after cleaning up its worktrees, then restart OpenCode.

## Verify on ReFS

Use the [disposable Windows fixture](../t3/smoke/README.md#prepare-a-fixture).
Its T3 launcher is not needed for this test. Open `V:\cow-smoke\repo` in OpenCode
v2 and create a worktree through its UI or API. Ask for the working directory,
`git status --short`, and `git rev-parse --git-common-dir`, leaving files unchanged.

From another PowerShell window:

```powershell
$worktree = '<directory reported by OpenCode>'
& V:\cow-smoke\tools\Verify-Worktree.ps1 `
  -FixtureRoot V:\cow-smoke -WorktreePath $worktree -RequireCow

$realGit = 'C:\Program Files\Git\cmd\git.exe'
$gitDir = (& $realGit -C $worktree rev-parse --absolute-git-dir).Trim()
Get-Content -LiteralPath (Join-Path $gitDir 'git-cow-opencode.json') -Raw |
  ConvertFrom-Json
```

The verifier checks tracked contents, Git metadata, clean status and all 64 MiB
of shared binary extents. Use `-RequireCow` for this native strategy check;
`-RequireTrace` checks shim dispatch and does not apply here. An ownership record
and shared extents establish that the plugin created a COW worktree.

## Development checks

```powershell
bun install --frozen-lockfile
$env:COW_TEST_EXECUTABLE = 'C:\Tools\git-cow-worktree\git-cow-worktree.exe'
$env:COW_TEST_GIT = 'C:\Program Files\Git\cmd\git.exe'
bun run typecheck
bun test
```

Tests use disposable repositories under the system temporary directory. Set
`COW_TEST_ROOT` to a ReFS directory to repeat them there. They exercise checkout
contents, refs, linked sources, ownership, dirty and committed cleanup, failed
checkout rollback and process-tree cancellation. They do not invoke a model.
