Download the archive for your operating system and CPU, then extract it. Go is not required to run these binaries. Git must be installed and available on `PATH`.

Windows downloads use `.zip`. Linux and macOS downloads use `.tar.gz`. Every archive includes the executable, README and MIT license. `checksums.txt` contains SHA-256 checksums for all archives.

On Windows, copy-on-write cloning requires the source and destination worktrees to share a ReFS volume with block cloning support. Other filesystems use ordinary Git checkout.

Run `git-cow-worktree --version` to check the installed version. Add the executable's folder to `PATH` to use `git cow-worktree add`.
