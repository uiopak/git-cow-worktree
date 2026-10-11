# Opt-in, process-local Git routing. Requires Windows and a native T3 executable.
[CmdletBinding()]
param(
    [string] $RealGit,
    [string] $CowPath,
    [string] $ShimPath,
    [string] $T3Path,
    [string[]] $T3Arguments = @(),
    [string] $ShimDirectory = (Join-Path $env:LOCALAPPDATA 'git-cow-worktree\t3-shim'),
    [switch] $CheckOnly,
    [switch] $Trace,
    [string] $TraceFile
)

$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'This launcher requires Windows.' }

function Resolve-NativeExecutable([string] $Value, [string] $Name, [string[]] $Candidates) {
    if ($Value) {
        $item = Get-Item -LiteralPath $Value -ErrorAction Stop
    } else {
        $item = $null
        foreach ($candidate in $Candidates) {
            if (Test-Path -LiteralPath $candidate -PathType Leaf) {
                $item = Get-Item -LiteralPath $candidate
                break
            }
        }
        if (-not $item) {
            $command = Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($command) { $item = Get-Item -LiteralPath $command.Source }
        }
    }
    if (-not $item -or $item.PSIsContainer -or $item.Extension -ine '.exe') {
        throw "Cannot resolve native $Name. Supply its executable path explicitly."
    }
    return $item.FullName
}

function Assert-Version([string] $Executable, [string] $Pattern) {
    $output = & $Executable --version 2>&1
    if ($LASTEXITCODE -ne 0 -or ($output -join "`n") -notmatch $Pattern) {
        throw "Version verification failed for '$Executable': $output"
    }
    return ($output -join "`n")
}

function Get-BinaryHash([string] $Path) {
    $stream = [System.IO.File]::OpenRead($Path)
    $algorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        return [System.BitConverter]::ToString($algorithm.ComputeHash($stream))
    } finally {
        $algorithm.Dispose()
        $stream.Dispose()
    }
}

# Framework PowerShell has no ProcessStartInfo.ArgumentList. Encode its native
# Windows argv using the CRT rules, never a shell command or Invoke-Expression.
function ConvertTo-NativeArgument([AllowEmptyString()][string] $Value) {
    if ($Value.Length -gt 0 -and $Value -notmatch '[\s"]') { return $Value }
    $builder = [System.Text.StringBuilder]::new()
    [void] $builder.Append('"')
    $slashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq '\') { $slashes++; continue }
        if ($character -eq '"') {
            [void] $builder.Append(('\' * (2 * $slashes + 1)))
        } else {
            [void] $builder.Append(('\' * $slashes))
        }
        [void] $builder.Append($character)
        $slashes = 0
    }
    [void] $builder.Append(('\' * (2 * $slashes)))
    [void] $builder.Append('"')
    return $builder.ToString()
}

$repositoryDirectory = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$ShimPath = Resolve-NativeExecutable $ShimPath 'git-cow-shim.exe' @(
    (Join-Path $PSScriptRoot 'git-cow-shim.exe'),
    (Join-Path $repositoryDirectory 'git-cow-shim.exe')
)
$CowPath = Resolve-NativeExecutable $CowPath 'git-cow-worktree.exe' @(
    (Join-Path (Split-Path $ShimPath -Parent) 'git-cow-worktree.exe'),
    (Join-Path $repositoryDirectory 'git-cow-worktree.exe')
)
$shimVersion = Assert-Version $ShimPath '^git-cow-shim\s'
$cowVersion = Assert-Version $CowPath '^git-cow-worktree\s'

if (-not $RealGit -and $env:GIT_COW_REAL_GIT) { $RealGit = $env:GIT_COW_REAL_GIT }
if (-not $RealGit) {
    # Prefer the unchanged Git installation over a process-local proxy on PATH.
    $gitCandidates = @()
    if ($env:ProgramFiles) { $gitCandidates += Join-Path $env:ProgramFiles 'Git\cmd\git.exe' }
    if (${env:ProgramFiles(x86)}) { $gitCandidates += Join-Path ${env:ProgramFiles(x86)} 'Git\cmd\git.exe' }
    if ($env:LOCALAPPDATA) { $gitCandidates += Join-Path $env:LOCALAPPDATA 'Programs\Git\cmd\git.exe' }
    $RealGit = Resolve-NativeExecutable '' 'git.exe' $gitCandidates
} else {
    $RealGit = Resolve-NativeExecutable $RealGit 'git.exe' @()
}
if ($RealGit -ieq $ShimPath -or (Get-BinaryHash $RealGit) -eq (Get-BinaryHash $ShimPath)) {
    throw 'RealGit resolves to the shim. Supply the unchanged Git executable.'
}
$gitVersion = Assert-Version $RealGit '^git version\s'
$T3Path = Resolve-NativeExecutable $T3Path 'T3 Code.exe' @(
    (Join-Path $env:LOCALAPPDATA 'Programs\T3 Code\T3 Code.exe'),
    (Join-Path $env:LOCALAPPDATA 'Programs\t3-code\T3 Code.exe')
)
$ShimDirectory = [System.IO.Path]::GetFullPath($ShimDirectory)
if ($TraceFile) { $TraceFile = [System.IO.Path]::GetFullPath($TraceFile) }
$proxyPath = Join-Path $ShimDirectory 'git.exe'
foreach ($source in @($RealGit, $ShimPath, $CowPath, $T3Path)) {
    if ($ShimDirectory.TrimEnd('\') -ieq [System.IO.Path]::GetDirectoryName($source).TrimEnd('\')) {
        throw 'ShimDirectory must be a dedicated directory separate from the source executables.'
    }
}

[pscustomobject]@{
    RealGit = $RealGit
    GitVersion = $gitVersion
    Cow = $CowPath
    CowVersion = $cowVersion
    Shim = $ShimPath
    ShimVersion = $shimVersion
    Proxy = $proxyPath
    T3 = $T3Path
    Arguments = $T3Arguments
    CheckOnly = [bool] $CheckOnly
    Trace = [bool] $Trace
    TraceFile = $TraceFile
}
if ($CheckOnly) { return }

# File tracing appends JSONL in the launched process. Require its parent to
# exist; neither the launcher nor the shim creates trace parent directories.
if ($TraceFile) {
    $traceParent = [System.IO.Path]::GetDirectoryName($TraceFile)
    if (-not (Test-Path -LiteralPath $traceParent -PathType Container)) {
        throw "TraceFile requires an existing parent folder: '$traceParent'. Create it before launching."
    }
    if (Test-Path -LiteralPath $TraceFile -PathType Container) {
        throw "TraceFile must name a file, not a folder: '$TraceFile'."
    }
}

# Refuse to overwrite an unrelated git.exe, even in the requested shim folder.
if (Test-Path -LiteralPath $ShimDirectory) {
    foreach ($entry in (Get-ChildItem -LiteralPath $ShimDirectory -Force)) {
        if ($entry.Name -ine 'git.exe' -or $entry.PSIsContainer) {
            throw "ShimDirectory '$ShimDirectory' contains unrelated files. Use a dedicated directory."
        }
    }
}
if (Test-Path -LiteralPath $proxyPath) {
    # A renamed proxy reports Git's version, so verify bytes rather than output.
    if ((Get-BinaryHash $proxyPath) -ne (Get-BinaryHash $ShimPath)) {
        throw "Existing '$proxyPath' differs from this shim. Use a new dedicated ShimDirectory."
    }
}
New-Item -ItemType Directory -Path $ShimDirectory -Force | Out-Null
if (-not (Test-Path -LiteralPath $proxyPath)) {
    Copy-Item -LiteralPath $ShimPath -Destination $proxyPath -Force
}

$names = @('PATH', 'GIT_COW_REAL_GIT', 'GIT_COW_SHIM_TRACE', 'GIT_COW_SHIM_TRACE_FILE', 'GIT_COW_SHIM_DEPTH', 'GIT_COW_SHIM_BYPASS')
$saved = @{}
foreach ($name in $names) { $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    [Environment]::SetEnvironmentVariable('PATH', "$ShimDirectory;$([IO.Path]::GetDirectoryName($CowPath));$($saved['PATH'])", 'Process')
    [Environment]::SetEnvironmentVariable('GIT_COW_REAL_GIT', $RealGit, 'Process')
    [Environment]::SetEnvironmentVariable('GIT_COW_SHIM_TRACE', $(if ($Trace) { '1' } else { $null }), 'Process')
    [Environment]::SetEnvironmentVariable('GIT_COW_SHIM_TRACE_FILE', $(if ($TraceFile) { $TraceFile } else { $null }), 'Process')
    [Environment]::SetEnvironmentVariable('GIT_COW_SHIM_DEPTH', $null, 'Process')
    [Environment]::SetEnvironmentVariable('GIT_COW_SHIM_BYPASS', $null, 'Process')
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $T3Path
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden
    if ($start.PSObject.Properties.Name -contains 'ArgumentList') {
        foreach ($argument in $T3Arguments) { $start.ArgumentList.Add($argument) }
    } else {
        $encoded = foreach ($argument in $T3Arguments) { ConvertTo-NativeArgument $argument }
        $start.Arguments = $encoded -join ' '
    }
    $process = [System.Diagnostics.Process]::Start($start)
    Write-Output "Launched T3 with process-local Git shim. PID=$($process.Id)"
    $process.Dispose()
} finally {
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
}
