# Creates only a new directory. It never replaces or removes an existing fixture.
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $Root,
    [string] $RealGit,
    [string] $CowPath,
    [string] $ShimPath,
    [string] $T3Path,
    [ValidateRange(1, 64)][int] $BinaryMiB = 16,
    [ValidateRange(1, 8)][int] $BinaryFiles = 4
)
. (Join-Path $PSScriptRoot 'Common.ps1')
if ($env:OS -ne 'Windows_NT') { throw 'This smoke fixture requires Windows and a ReFS volume.' }
$Root = [IO.Path]::GetFullPath($Root)
if (Test-Path -LiteralPath $Root) { throw "Fixture root already exists: $Root. Choose a new directory." }
$volumeRoot = [IO.Path]::GetPathRoot($Root)
if ($volumeRoot -notmatch '^[A-Za-z]:\\$') { throw 'Use a local drive path for the fixture.' }
$volume = Get-Volume -DriveLetter $volumeRoot.Substring(0, 1)
if ($volume.FileSystem -ne 'ReFS') { throw "The fixture requires ReFS; this drive uses $($volume.FileSystem)." }
$dataBytes = [long] $BinaryMiB * $BinaryFiles * 1MB
if ($volume.SizeRemaining -lt (3 * $dataBytes + 512MB)) { throw 'Insufficient free space for the source, Git blobs and ordinary control.' }
$RealGit = Get-SmokeExecutable $RealGit 'git.exe'
$CowPath = Get-SmokeExecutable $CowPath 'git-cow-worktree.exe'
$ShimPath = Get-SmokeExecutable $ShimPath 'git-cow-shim.exe'
if ($T3Path) { $T3Path = Get-SmokeExecutable $T3Path 'T3 Code.exe' }
$repo = Join-Path $Root 'repo'
foreach ($relative in @('', 'repo', 'repo\assets', 'controls', 'worktrees', 'logs', 'tools', 'hooks')) {
    [IO.Directory]::CreateDirectory((Join-Path $Root $relative)) | Out-Null
}
foreach ($name in @('README.md', 'Common.ps1', 'ExtentProbe.cs', 'Verify-Worktree.ps1', 'Prepare-SmokeRepo.ps1', 'Start-Smoke.ps1')) {
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $Root "tools\$name")
}
$genericLauncher = Join-Path $PSScriptRoot 'Start-T3WithCow.ps1'
if (-not (Test-Path -LiteralPath $genericLauncher)) { $genericLauncher = Join-Path (Split-Path $PSScriptRoot -Parent) 'Start-T3WithCow.ps1' }
Copy-Item -LiteralPath $genericLauncher -Destination (Join-Path $Root 'tools\Start-T3WithCow.ps1')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'README.md') -Destination (Join-Path $Root 'README.md')
Write-SmokeText (Join-Path $Root 'Start-T3Smoke.ps1') @'
[CmdletBinding()]
param([string] $T3Path, [switch] $CheckOnly, [switch] $Trace)
& (Join-Path $PSScriptRoot 'tools\Start-Smoke.ps1') -FixtureRoot $PSScriptRoot -T3Path $T3Path -CheckOnly:$CheckOnly -Trace:$Trace
'@
Write-SmokeText (Join-Path $Root 'Verify-Current.ps1') @'
[CmdletBinding()]
param([string] $WorktreePath = (Get-Location).Path, [switch] $AllowEdits)
& (Join-Path $PSScriptRoot 'tools\Verify-Worktree.ps1') -FixtureRoot $PSScriptRoot -WorktreePath $WorktreePath -RequireCow -RequireTrace -AllowEdits:$AllowEdits
'@
Invoke-SmokeGit $RealGit @('init', '-b', 'main', $repo) | Out-Null
foreach ($setting in @(
    @('user.name', 'COW smoke test'), @('user.email', 'cow-smoke@example.invalid'),
    @('commit.gpgsign', 'false'), @('core.autocrlf', 'false'), @('core.eol', 'lf'),
    @('core.hooksPath', (Join-Path $Root 'hooks'))
)) {
    Invoke-SmokeGit $RealGit @('-C', $repo, 'config', '--local', $setting[0], $setting[1]) | Out-Null
}
Write-SmokeText (Join-Path $repo '.gitattributes') "* !filter !ident`n*.bin -text`n*.md text eol=lf`n*.txt text eol=lf`n"
Write-SmokeText (Join-Path $repo '.gitignore') ".env`n"
Write-SmokeText (Join-Path $repo 'hello.txt') "Hello from the COW smoke repository.`n"
Write-SmokeText (Join-Path $repo 'notes with spaces.txt') "This filename checks argument and path handling.`n"
Write-SmokeText (Join-Path $repo ("unicode-{0}.txt" -f [char]0x03bb)) "This filename checks Unicode path handling.`n"
Write-SmokeText (Join-Path $repo 'README.md') @"
# COW smoke repository

This disposable repository has $BinaryFiles dense binary files of $BinaryMiB MiB each.
Leave assets/*.bin unchanged during creation checks and the ordinary text edit test.
For an explicitly requested binary isolation test, change only the requested
bytes in the worktree's payload file and leave the source checkout unchanged.

For an agent edit test, add one sentence to hello.txt and commit it.
The application should create the worktree before the agent starts.
The verification tools and reports are outside the Git checkout, under $Root.
"@
Write-SmokeText (Join-Path $repo 'AGENTS.md') @"
# Smoke test instructions

Leave files unchanged during the application provisioning check.
For the ordinary text edit test, change only hello.txt as requested.
For an explicitly requested binary write-isolation test, change only the
specified bytes in the specified assets/*.bin file in this worktree. Leave the
source checkout and all other files unchanged. Do not commit unless requested.
These test defaults do not override the user's explicit instructions.
Do not create or remove worktrees yourself during the application provisioning test.
Use ordinary Git for status, diff and commits.
"@
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$buffer = New-Object byte[] 1MB
try {
    for ($i = 1; $i -le $BinaryFiles; $i++) {
        $path = Join-Path $repo ("assets\payload-{0:D2}.bin" -f $i)
        $stream = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try {
            for ($chunk = 0; $chunk -lt $BinaryMiB; $chunk++) { $rng.GetBytes($buffer); $stream.Write($buffer, 0, $buffer.Length) }
            $stream.Flush($true)
        } finally { $stream.Dispose() }
    }
} finally { $rng.Dispose() }
Invoke-SmokeGit $RealGit @('-C', $repo, 'add', '--all') | Out-Null
Invoke-SmokeGit $RealGit @('-C', $repo, 'commit', '-m', 'Create disposable COW smoke fixture') | Out-Null
$head = (Invoke-SmokeGit $RealGit @('-C', $repo, 'rev-parse', 'HEAD') -join '').Trim()
$oldTime = [DateTime]::new(2020, 1, 2, 3, 4, 5, [DateTimeKind]::Utc)
$files = @(Get-ChildItem -LiteralPath $repo -Recurse -File | Where-Object { $_.FullName -notlike "$repo\.git\*" } | ForEach-Object {
    $_.LastWriteTimeUtc = $oldTime
    [pscustomobject]@{
        path = $_.FullName.Substring($repo.Length + 1).Replace('\', '/')
        bytes = $_.Length; sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
        binary = $_.Extension -eq '.bin'; mtimeTicks = $oldTime.Ticks
    }
})
# Refresh Git's stat cache after assigning the old timestamps. diff-files used
# in source ranking treats an unrefreshed stat mismatch as a changed file.
$sourceStatus = @(Invoke-SmokeGit $RealGit @('-C', $repo, 'status', '--porcelain=v1'))
if ($sourceStatus.Count) { throw 'The source repository is unexpectedly dirty.' }
$manifest = [pscustomobject]@{
    kind = 'git-cow-worktree-smoke-v1'; root = $Root; repo = $repo; head = $head
    realGit = $RealGit; cow = $CowPath; dataBytes = $dataBytes
    shim = $ShimPath; t3 = $T3Path
    traceFile = (Join-Path $Root 'logs\shim.jsonl'); files = $files
}
Write-SmokeText (Join-Path $Root 'manifest.json') ($manifest | ConvertTo-Json -Depth 6)

$plain = Join-Path $Root 'controls\plain'
$cow = Join-Path $Root 'controls\cow'
Invoke-SmokeGit $RealGit @('-C', $repo, 'worktree', 'add', '--detach', $plain, 'HEAD') | Out-Null
Push-Location -LiteralPath $repo
try {
    # Windows PowerShell represents redirected native stderr as ErrorRecords.
    # Git's progress output is normal; decide success from the native exit code.
    $savedPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $cowOutput = @(& $CowPath add -v --detach $cow HEAD 2>&1)
        $cowCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $savedPreference }
    Write-SmokeText (Join-Path $Root 'logs\cow-control.txt') (($cowOutput | ForEach-Object { $_.ToString() }) -join "`n")
    if ($cowCode -ne 0) { throw "COW control creation failed with exit code $cowCode." }
} finally { Pop-Location }
$plainReport = & (Join-Path $Root 'tools\Verify-Worktree.ps1') -FixtureRoot $Root -WorktreePath $plain
$cowReport = & (Join-Path $Root 'tools\Verify-Worktree.ps1') -FixtureRoot $Root -WorktreePath $cow -RequireCow
if ($plainReport.sharedBytes -ne 0) { throw 'Ordinary control unexpectedly shares extents with the source.' }

# Mutate a separate, newly created control to check allocation-on-write isolation.
$isolation = [IO.Path]::GetFullPath((Join-Path $Root 'controls\isolation'))
if (-not $isolation.StartsWith($Root.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Isolation path escapes fixture root.' }
Push-Location -LiteralPath $repo
try {
    & $CowPath add --detach $isolation HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Isolation control creation failed.' }
} finally { Pop-Location }
$firstBinary = $files | Where-Object binary | Select-Object -First 1
$sourceFile = Join-Path $repo $firstBinary.path
$targetFile = Join-Path $isolation $firstBinary.path
$stream = [IO.File]::Open($targetFile, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
try { $firstByte = $stream.ReadByte(); $stream.Position = 0; $stream.WriteByte([byte]($firstByte -bxor 255)); $stream.Flush() } finally { $stream.Dispose() }
$sourceUnchanged = (Get-FileHash -LiteralPath $sourceFile -Algorithm SHA256).Hash -eq $firstBinary.sha256
$targetChanged = (Get-FileHash -LiteralPath $targetFile -Algorithm SHA256).Hash -ne $firstBinary.sha256
Write-SmokeText (Join-Path $Root 'logs\isolation.json') ([pscustomobject]@{ sourceUnchanged = $sourceUnchanged; targetChanged = $targetChanged; path = $firstBinary.path } | ConvertTo-Json)
if (-not $sourceUnchanged -or -not $targetChanged) { throw 'COW file isolation check failed. The isolation control remains for inspection.' }
# Both the absolute path check above and creation succeeded before this removal.
Invoke-SmokeGit $RealGit @('-C', $repo, 'worktree', 'remove', '--force', $isolation) | Out-Null
Write-Host "Prepared $repo. Ordinary shared bytes: $($plainReport.sharedBytes). COW shared bytes: $($cowReport.sharedBytes)."
return $manifest
