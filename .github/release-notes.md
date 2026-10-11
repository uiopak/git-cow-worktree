Download the archive for your operating system and CPU, then extract it. Go is not required to run these binaries. Git must be installed and available on `PATH`.

Windows downloads use `.zip`. Linux and macOS downloads use `.tar.gz`. `checksums.txt` contains SHA-256 checksums for all seven archives.

This release adds automatic COW worktree integration for AI coding tools:

- Claude Code uses native `WorktreeCreate` and `WorktreeRemove` hooks through the compiled adapter. Hook settings are included.
- OpenCode v2 uses a native worktree strategy plugin, targeting `2.0.26`. Plugin source, ownership tracking and setup instructions are included under `integrations/opencode`.
- T3 Code and the Windows ChatGPT/Codex app use an optional `git-cow-shim.exe` with application launchers. Normal Git commands pass through unchanged. COW child processes are contained in a Windows Job Object for cancellation.
- Antigravity 2 uses an experimental trace launcher. Native New Worktree creation was verified with `2.22.0` after redirecting its worktree storage to ReFS through a directory junction. All 64 MiB shared with the source. A one-byte edit made one 4 KiB cluster private while leaving the source unchanged. Isolated subagents, GUI cleanup and cancellation still need separate live tests.

Automatic GUI creation was verified on Windows ReFS in T3, Claude, ChatGPT/Codex, OpenCode v2 and Antigravity 2. These checks cover the documented creation flows and tested versions. See `docs/integrations.md` in the archive for configuration, desktop shortcuts, updates and limitations.

Windows archives include a disposable ReFS smoke fixture with physical extent and isolation checks. The shim can record JSONL dispatch and exit events, and the trace launchers also capture Git's own commands.

Failed checkouts now roll back their owned worktree and unchanged new branch. Post-checkout hooks run once, with their exit status preserved.

On Windows, copy-on-write cloning requires the source and destination worktrees to share a ReFS volume with block cloning support. Other filesystems use ordinary Git checkout.

Run `git-cow-worktree --version` to check the installed version. Add the executable's folder to `PATH` to use `git cow-worktree add`.
