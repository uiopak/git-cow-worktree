`git-cow-worktree` is a drop-in replacement for `git worktree add`.

It uses copy-on-write for the worktree, reducing disk usage.

It is entirely vibe-coded, but there's also not that much to it. Use at your own risk.

Everything below is LLM-written.

---

## Install

Download a binary from the [latest GitHub release](https://github.com/uiopak/git-cow-worktree/releases/latest). Go is not required. Git must be installed and available on `PATH`.

Choose the archive for your system and CPU:

| System | Archive suffix |
| --- | --- |
| Windows, Intel or AMD 64-bit | `windows_amd64.zip` |
| Windows, ARM64 | `windows_arm64.zip` |
| Windows, 32-bit | `windows_386.zip` |
| Linux, Intel or AMD 64-bit | `linux_amd64.tar.gz` |
| Linux, ARM64 | `linux_arm64.tar.gz` |
| macOS, Intel | `darwin_amd64.tar.gz` |
| macOS, Apple silicon | `darwin_arm64.tar.gz` |

For Windows on an Intel or AMD 64-bit PC, download and extract the latest binary in PowerShell:

```powershell
$release = Invoke-RestMethod 'https://api.github.com/repos/uiopak/git-cow-worktree/releases/latest'
$asset = $release.assets | Where-Object name -Like '*_windows_amd64.zip'
Invoke-WebRequest $asset.browser_download_url -OutFile $asset.name -UseBasicParsing
Expand-Archive -LiteralPath $asset.name -DestinationPath .\git-cow-worktree -Force
.\git-cow-worktree\git-cow-worktree.exe --version
```

Extract the archive into a new directory and add the executable's folder to `PATH`. Git can then dispatch `git cow-worktree ...`. On Linux and macOS, the archived binary already has executable permissions. Each release includes `checksums.txt` for verifying the downloaded archives.

### Build from source

Building from source requires the Go version specified in `go.mod` or newer:

```sh
git clone https://github.com/uiopak/git-cow-worktree.git
cd git-cow-worktree
go build -o git-cow-worktree .
```

On Windows, use `go build -o git-cow-worktree.exe .`.

## Usage

```sh
git cow-worktree add [git-worktree-add flags] [--from <path>] [-v] <path> [<commit-ish>]
```

Examples:

```sh
git cow-worktree add ../repo-feature feature
git cow-worktree add -b topic ../repo-topic main
git cow-worktree add -v --from ../repo-main ../repo-topic topic
```

Most flags are inherited from `git worktree add`.

Added by `git-cow-worktree`:

- `--from <path>`: use a specific source worktree instead of auto-selecting one.
- `-v`, `--verbose`: print the chosen source, clone counts, and timings.

Special case:

- `--no-checkout`: passed through to `git worktree add`; no reflinking is attempted.

## Filesystem support

Reflinking is supported on APFS on macOS, on Linux filesystems with `FICLONE` support such as btrfs, XFS, and bcachefs, and on Windows ReFS volumes with block cloning enabled. On unsupported filesystems, including NTFS, or across volumes, `git-cow-worktree` falls back to Git's normal checkout behavior.

On Windows, the source and new worktree must be on the same ReFS volume. Files are cloned individually with [`FSCTL_DUPLICATE_EXTENTS_TO_FILE`](https://learn.microsoft.com/en-us/windows-server/storage/refs/block-cloning). Small files and partial final clusters are supported, as are sparse files and integrity streams. The command preserves Git for Windows index metadata so checkout keeps the clones.

Run from your repository after installing the executable on `PATH`:

```powershell
git cow-worktree add -v --detach V:\repo-feature HEAD
```

Replace `V:\repo-feature` with a new path on your repository's ReFS volume. Git for Windows must be on `PATH`.

## Scope

`git-cow-worktree` only replaces `git worktree add`. Other `git worktree` subcommands should be run with Git directly.

## How it works

It first runs `git worktree add --no-checkout`, so Git creates the new worktree metadata but leaves the working tree empty.

Then it picks a source worktree. Candidates are the current worktree, the main worktree, and a few recently modified other worktrees. It ignores the target worktree and unmaterialized worktrees. Candidates are scored by `git rev-list --left-right --count <source>...<target>`; fewer commits ahead/behind is better.

It runs `git ls-tree -r -t` on the source and target commits and plans the cheapest way to materialize each path. A directory whose tree SHA is the same on both sides has identical tracked contents. APFS can clone that whole directory in one syscall. Otherwise the planner descends and reflinks matching files individually, which is how Linux and Windows always work. Paths that are dirty, untracked, ignored, or submodules in the source are skipped, along with symlinks and mode mismatches.

Cloned files are verified before their entries and stat data are written to the new worktree's index. Files without checkout conversions are hashed against their blobs in parallel. LF/CRLF conversion uses [Git's filtered checkout output](https://git-scm.com/docs/git-cat-file) and the target commit's attributes, so matching CRLF files keep their sharing and changed rules produce the correct bytes. Custom filters, `ident`, working-tree encodings, and NUL-containing forced text files are left to the final checkout.

Older Git versions use a temporary index to inspect target attributes. If Git lacks the batch features needed for conversion checks, those files use ordinary checkout. Explicit `attr.tree` or `GIT_ATTR_SOURCE` overrides also use ordinary checkout.

The seeded index preserves the sharing. Git decides whether to rewrite a working-tree file by comparing it against its index entry. A worktree with no index, which is what `--no-checkout` leaves behind, makes checkout rewrite every file and undo every clone.

Finally it runs Git checkout in the new worktree. That fills in files that were not cloned, overwrites anything stale, and produces the same index a plain checkout would. Every step before it is best-effort: if reflinks are unsupported, if the source can't be inspected, or if the index can't be written, the checkout still runs and just becomes a normal one.

## Testing on Windows

Run `go test ./...` with `TMP` and `TEMP` pointing at an existing temporary directory on ReFS to exercise real block cloning. The Windows tests cover clone isolation, cluster boundaries, sparse files larger than 4 GiB, integrity streams, and Git index metadata. Run the tests with a temporary directory on NTFS to check ordinary-checkout fallback.

## Release automation

GitHub Actions tests changes on Windows, Linux and macOS, then builds all seven binary archives. Pushes to `main`, pull requests and manual workflow runs provide downloadable build artifacts. GitHub's hosted Windows runner checks NTFS fallback; the native ReFS tests need a ReFS temporary directory as described above.

Push a version tag to publish a GitHub release automatically:

```sh
git tag -a v0.0.1 -m "Release v0.0.1"
git push origin v0.0.1
```

Commit and push the release's source changes before tagging. Use the next version for later releases. Publication waits for every test and build, verifies archive checksums, then publishes the release with Windows ZIP files, Linux and macOS tarballs, and `checksums.txt`. Tags such as `v0.0.2-rc.1` publish prereleases.
