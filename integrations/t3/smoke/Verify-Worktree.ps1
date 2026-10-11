# Read-only checks of a worktree against the disposable fixture's initial commit.
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $FixtureRoot,
    [string] $WorktreePath = (Get-Location).Path,
    [string] $TraceFile,
    [switch] $RequireCow,
    [switch] $RequireTrace,
    [switch] $AllowEdits
)
. (Join-Path $PSScriptRoot 'Common.ps1')
$FixtureRoot = [IO.Path]::GetFullPath($FixtureRoot)
$WorktreePath = (Get-Item -LiteralPath $WorktreePath).FullName
$manifest = Read-SmokeManifest $FixtureRoot
if (-not $TraceFile) { $TraceFile = $manifest.traceFile }
$TraceFile = [IO.Path]::GetFullPath($TraceFile)
$git = Get-SmokeExecutable $manifest.realGit 'git.exe'
$failures = [Collections.Generic.List[string]]::new()
$common = (Invoke-SmokeGit $git @('-C', $WorktreePath, 'rev-parse', '--path-format=absolute', '--git-common-dir') -join '').Trim()
$top = (Invoke-SmokeGit $git @('-C', $WorktreePath, 'rev-parse', '--show-toplevel') -join '').Trim()
$prefix = (Invoke-SmokeGit $git @('-C', $WorktreePath, 'rev-parse', '--show-prefix') -join '')
$gitDir = (Invoke-SmokeGit $git @('-C', $WorktreePath, 'rev-parse', '--path-format=absolute', '--git-dir') -join '').Trim()
$head = (Invoke-SmokeGit $git @('-C', $WorktreePath, 'rev-parse', 'HEAD') -join '').Trim()
$status = @(Invoke-SmokeGit $git @('-C', $WorktreePath, 'status', '--porcelain=v1', '--untracked-files=all'))
if ($common.Replace('/', '\').TrimEnd('\') -ine (Join-Path $manifest.repo '.git')) {
    $failures.Add('Worktree does not belong to the smoke repository.')
}
# Git resolves parent junctions in --show-toplevel. An empty prefix establishes
# that the supplied path is the root, and a separate git-dir establishes that
# it is a linked worktree, including when its logical and physical paths differ.
if ($prefix -ne '' -or $gitDir.Replace('/', '\').TrimEnd('\') -ieq $common.Replace('/', '\').TrimEnd('\')) {
    $failures.Add('Target must be a separate linked worktree root.')
}
if (-not $AllowEdits -and $head -ne $manifest.head) { $failures.Add('HEAD differs from the initial fixture commit.') }
if (-not $AllowEdits -and $status.Count -ne 0) { $failures.Add('Worktree has local changes.') }

$extentAvailable = $true
$extentError = $null
try { Initialize-ExtentProbe } catch { $extentAvailable = $false; $extentError = $_.Exception.Message }
$files = @()
$dataBytes = [long] 0
$sharedBytes = [long] 0
foreach ($entry in $manifest.files) {
    $target = Join-Path $WorktreePath $entry.path
    $exists = Test-Path -LiteralPath $target -PathType Leaf
    $hashMatches = $false
    $shared = [long] 0
    $queryError = $null
    $mtimePreserved = $false
    if ($exists) {
        $item = Get-Item -LiteralPath $target
        $hashMatches = $item.Length -eq $entry.bytes -and (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -eq $entry.sha256
        $mtimePreserved = $item.LastWriteTimeUtc.Ticks -eq $entry.mtimeTicks
        if ($entry.binary -and $extentAvailable) {
            try {
                $sourceLayout = [CowSmoke.ExtentProbe]::Read((Join-Path $manifest.repo $entry.path))
                $targetLayout = [CowSmoke.ExtentProbe]::Read($target)
                $shared = [CowSmoke.ExtentProbe]::SharedBytes($sourceLayout, $targetLayout)
            } catch { $queryError = $_.Exception.Message }
        }
    }
    if (-not $hashMatches -and ($entry.binary -or -not $AllowEdits)) { $failures.Add("Contents differ: $($entry.path)") }
    if ($entry.binary) { $dataBytes += $entry.bytes; $sharedBytes += $shared }
    $files += [pscustomobject]@{
        path = $entry.path; exists = $exists; hashMatches = $hashMatches
        binary = $entry.binary; bytes = $entry.bytes; mtimePreserved = $mtimePreserved
        sharedBytes = $shared; extentError = $queryError
    }
}
if ($RequireCow -and $sharedBytes -ne $dataBytes) { $failures.Add('The binary fixture files do not share all their extents with the source.') }

$traceMatches = @()
$traceErrors = 0
$tracePathMatches = @{}
if (Test-Path -LiteralPath $TraceFile -PathType Leaf) {
    # A concurrently appended final line can be incomplete. Skip it and report the count.
    # Share write access so reading the trace cannot make concurrent appenders fail.
    $traceStream = [IO.File]::Open($TraceFile, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
    $traceReader = [IO.StreamReader]::new($traceStream, [Text.Encoding]::UTF8)
    try {
        while ($null -ne ($line = $traceReader.ReadLine())) {
            try { $record = $line | ConvertFrom-Json } catch { $traceErrors++; continue }
            if ($record.event -ne 'exit' -or $record.route -ne 'cow' -or $record.exitCode -ne 0) { continue }
            $commandIndex = -1
            for ($i = 0; $i + 1 -lt $record.argv.Count; $i++) {
                if ($record.argv[$i] -in @('worktree', 'cow-worktree') -and $record.argv[$i + 1] -eq 'add') {
                    $commandIndex = $i
                    break
                }
            }
            if ($commandIndex -lt 0) { continue }
            # Ignore global -C source paths. Compare checkout arguments, accepting
            # a logical junction path that addresses the same .git metadata file.
            foreach ($argument in @($record.argv | Select-Object -Skip ($commandIndex + 2))) {
                if ($argument -and $argument.Replace('/', '\').TrimEnd('\') -ieq $WorktreePath.TrimEnd('\')) {
                    $traceMatches += $record
                    break
                }
                if (-not $extentAvailable -or -not $argument -or -not [IO.Path]::IsPathRooted($argument)) { continue }
                if (-not $tracePathMatches.ContainsKey($argument)) {
                    $tracePathMatches[$argument] = $false
                    $metadata = Join-Path $argument '.git'
                    if (Test-Path -LiteralPath $metadata -PathType Leaf) {
                        try { $tracePathMatches[$argument] = [CowSmoke.ExtentProbe]::SameFile($metadata, (Join-Path $WorktreePath '.git')) } catch { }
                    }
                }
                if ($tracePathMatches[$argument]) {
                    $traceMatches += $record
                    break
                }
            }
        }
    } finally { $traceReader.Dispose() }
}
if ($RequireTrace -and $traceMatches.Count -eq 0) { $failures.Add('No successful COW shim dispatch for this worktree was found in the trace file.') }

$report = [pscustomobject]@{
    timestamp = [DateTime]::UtcNow.ToString('o'); worktree = $WorktreePath
    commonDir = $common; gitDir = $gitDir; topLevel = $top
    head = $head; status = $status; contentBaseline = $manifest.head
    dataBytes = $dataBytes; sharedBytes = $sharedBytes
    extentProbeError = $extentError; successfulCowDispatches = $traceMatches.Count
    traceFile = $TraceFile; invalidTraceLines = $traceErrors
    files = $files; failures = @($failures.ToArray()); passed = $failures.Count -eq 0
}
$reportPath = Join-Path $FixtureRoot ("logs\verify-{0}-{1}.json" -f [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss'), [Guid]::NewGuid().ToString('N').Substring(0, 8))
Write-SmokeText $reportPath ($report | ConvertTo-Json -Depth 8)
Write-Host "Worktree: $WorktreePath"
Write-Host "Binary data: $dataBytes bytes; shared with source: $sharedBytes bytes"
Write-Host "Successful COW shim dispatches: $($traceMatches.Count)"
Write-Host "Report: $reportPath"
if ($failures.Count) { throw ($failures -join "`n") }
return $report
