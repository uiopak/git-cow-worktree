# T3 COW smoke test on Windows

The preparation script creates a disposable Git repository with 64 MiB of dense binary data, an ordinary linked-worktree control and a COW control. It checks tracked contents and Git metadata, then compares the files' disk extents. It also mutates one byte in a separate temporary COW control and checks that the source stays unchanged.

The extent probe uses [FSCTL_GET_RETRIEVAL_POINTERS](https://learn.microsoft.com/en-us/windows/win32/api/winioctl/ni-winioctl-fsctl_get_retrieval_pointers) to read VCN-to-LCN mappings. Equal LCNs at equal file offsets, on the same volume and with distinct file IDs, show shared clusters. This follows [ReFS's block-cloning model](https://learn.microsoft.com/en-us/windows-server/storage/refs/block-cloning). Timestamps are reported as additional evidence; they do not determine whether sharing passed.

The probe resolves the opened file's [volume GUID path](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getfinalpathnamebyhandlew) before querying cluster size. This keeps extent measurements on the actual volume when a parent junction redirects a logical `C:` path to ReFS on another drive.

## Prepare a fixture

Extract a Windows release archive. From its root, run:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\integrations\t3\smoke\Prepare-SmokeRepo.ps1 `
  -Root 'V:\cow-smoke' `
  -CowPath (Resolve-Path .\git-cow-worktree.exe).Path `
  -ShimPath (Resolve-Path .\git-cow-shim.exe).Path `
  -T3Path "$env:LOCALAPPDATA\Programs\t3code\T3 Code (Nightly).exe"
```

Supply your actual T3 executable path. `-RealGit` selects a non-default Git installation. The root must be a new local directory on ReFS. The script refuses an existing root. `-BinaryMiB` and `-BinaryFiles` control fixture size; their defaults are 16 and 4. Go is not needed to run the fixture or either binary. The extent helper compiles through PowerShell's built-in .NET support.

The source is `<root>\repo`. Controls are `<root>\controls\plain` and `<root>\controls\cow`. Tools, the initial commit and hash manifest, and verification reports live outside the source checkout. The fixture has no remote.

Preparation fails if the COW control does not share all its binary extents, the ordinary control shares extents, file contents differ, or the isolation check fails. A failed preparation leaves its files and reports for inspection. Choose a fresh root to run preparation again.

## Test T3 with Codex as its provider

1. Add `<root>\repo` as a T3 project, with the thread environment set to **Worktree**. Select Codex as the provider.
2. Set **Settings → Storage → Worktree location** to `<root>\worktrees` on the same ReFS volume.
3. Exit T3 completely. Check the prepared launcher, then run it without `-CheckOnly`:

   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File V:\cow-smoke\Start-T3Smoke.ps1 -CheckOnly
   powershell -NoProfile -ExecutionPolicy Bypass -File V:\cow-smoke\Start-T3Smoke.ps1
   ```

4. Create a new worktree thread in the smoke project. T3 should provision it before the agent starts. Run the verifier from that worktree, either as a project action or in its terminal:

   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File V:\cow-smoke\Verify-Current.ps1
   ```

5. Ask the agent to add one sentence to `hello.txt` and commit it. Leave `assets/*.bin` unchanged. Check again with `-AllowEdits` to permit text changes and a new HEAD while still requiring intact, shared binary files:

   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File V:\cow-smoke\Verify-Current.ps1 -AllowEdits
   ```

Both verifiers require a successful `route=cow` dispatch for the target path in `<root>\logs\shim.jsonl`, correct Git common-directory identity, matching binary contents, and all binary extents shared with the source. The initial check also requires the original HEAD, all original tracked contents and a clean status. Supply `-WorktreePath` when running outside the target directory.

Parent directory junctions are supported. Git may report the physical path in `--show-toplevel`; the verifier checks the root using `--show-prefix` and requires linked-worktree metadata. It also accepts a trace path that addresses the same `.git` file through a junction. It rejects the main checkout, worktree subdirectories and records that mention a worktree only as the global `-C` source.

The launcher uses a dedicated `<root>\shim\git.exe` and limits PATH changes to the launched app. It refuses to launch a second instance of the specified T3 executable. File logging is enabled by default for this fixture. `-Trace` also enables stderr diagnostics. Starting T3 normally after exiting the test instance disables routing.

T3 can reconstruct PATH from shell profiles. If the verifier reports no successful shim dispatch, inspect the JSONL file and T3's environment. If routing succeeds but shared bytes are zero, check that the destination is on the source's ReFS volume. The two checks diagnose different failures.

This test covers worktrees that T3 provisions for its Codex provider. Standalone Codex can open a pre-created COW control. Codex's separate managed `--no-checkout` followed by `reset --hard` workflow requires another integration.
