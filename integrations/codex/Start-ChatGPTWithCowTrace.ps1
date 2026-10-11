# Process-local Git tracing. Discovers ChatGPT by default; AppPath also supports
# other desktop clients without changing their Git installations or saved PATH.
[CmdletBinding()]
param(
    [string] $AppPath,
    [string[]] $AppArguments = @(),
    [string] $RealGit,
    [string] $CowPath,
    [string] $ShimPath,
    [string] $ShimDirectory,
    [string] $LogDirectory = (Join-Path $env:LOCALAPPDATA 'git-cow-worktree\chatgpt-traces'),
    [switch] $CheckOnly,
    [switch] $PrepareOnly,
    [switch] $PassThrough
)

$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'This launcher requires Windows.' }
if ($CheckOnly -and $PrepareOnly) { throw 'Choose either CheckOnly or PrepareOnly.' }
# A process started from PowerShell 7 can inherit its module path when launching
# Windows PowerShell 5.1. Load this host's utility module explicitly so hashing
# and JSON output do not depend on an incompatible inherited module path.
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Utility') -Scope Local -ErrorAction Stop

function Resolve-Executable([string] $Value, [string] $Name, [string[]] $Candidates) {
    if (-not $Value) {
        foreach ($candidate in $Candidates) {
            if (Test-Path -LiteralPath $candidate -PathType Leaf) { $Value = $candidate; break }
        }
        if (-not $Value) {
            $command = Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($command) { $Value = $command.Source }
        }
    }
    if (-not $Value) { throw "Cannot resolve $Name. Supply its executable path." }
    $item = Get-Item -LiteralPath $Value
    if ($item.PSIsContainer -or $item.Extension -ine '.exe') { throw "Expected a native executable: '$Value'." }
    return $item.FullName
}

function Get-Version([string] $Path, [string] $Pattern) {
    $value = (& $Path --version 2>&1) -join "`n"
    if ($LASTEXITCODE -ne 0 -or $value -notmatch $Pattern) { throw "Version verification failed for '$Path': $value" }
    return $value
}

# Windows PowerShell 5.1 requires CRT argument quoting instead of ArgumentList.
function ConvertTo-NativeArgument([AllowEmptyString()][string] $Value) {
    if ($Value.Length -gt 0 -and $Value -notmatch '[\s"]') { return $Value }
    $builder = [Text.StringBuilder]::new()
    [void] $builder.Append('"')
    $slashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq '\') { $slashes++; continue }
        if ($character -eq '"') { [void] $builder.Append(('\' * (2 * $slashes + 1))) }
        else { [void] $builder.Append(('\' * $slashes)) }
        [void] $builder.Append($character)
        $slashes = 0
    }
    [void] $builder.Append(('\' * (2 * $slashes)))
    [void] $builder.Append('"')
    return $builder.ToString()
}

function Set-ChildEnvironment($StartInfo, [hashtable] $Values) {
    foreach ($name in $Values.Keys) {
        if ($null -eq $Values[$name]) { $StartInfo.EnvironmentVariables.Remove($name) }
        else { $StartInfo.EnvironmentVariables[$name] = $Values[$name] }
    }
}

$packageVersion = $null
if (-not $AppPath) {
    # Resolve at every launch; the WindowsApps version directory changes on updates.
    foreach ($packageName in @('OpenAI.Codex', 'OpenAI.ChatGPT')) {
        $packages = @(Get-AppxPackage -Name $packageName | Sort-Object Version -Descending)
        foreach ($package in $packages) {
            [xml] $manifest = Get-Content -LiteralPath (Join-Path $package.InstallLocation 'AppxManifest.xml') -Raw
            foreach ($application in $manifest.Package.Applications.Application) {
                if ([IO.Path]::GetFileName([string] $application.Executable) -ine 'ChatGPT.exe') { continue }
                $AppPath = Join-Path $package.InstallLocation ([string] $application.Executable)
                $packageVersion = [string] $package.Version
                break
            }
            if ($AppPath) { break }
        }
        if ($AppPath) { break }
    }
    if (-not $AppPath) { throw 'Cannot discover the installed ChatGPT Windows package. Supply AppPath explicitly.' }
}
$AppPath = Resolve-Executable $AppPath 'ChatGPT.exe' @()
$appName = [IO.Path]::GetFileNameWithoutExtension($AppPath)
$traceName = $appName.ToLowerInvariant() -replace '[^a-z0-9-]', '-'
$archiveRoot = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$ShimPath = Resolve-Executable $ShimPath 'git-cow-shim.exe' @((Join-Path $archiveRoot 'git-cow-shim.exe'))
$CowPath = Resolve-Executable $CowPath 'git-cow-worktree.exe' @((Join-Path (Split-Path $ShimPath -Parent) 'git-cow-worktree.exe'))
$gitCandidates = @()
if ($env:ProgramFiles) { $gitCandidates += Join-Path $env:ProgramFiles 'Git\cmd\git.exe' }
if (${env:ProgramFiles(x86)}) { $gitCandidates += Join-Path ${env:ProgramFiles(x86)} 'Git\cmd\git.exe' }
if ($env:LOCALAPPDATA) { $gitCandidates += Join-Path $env:LOCALAPPDATA 'Programs\Git\cmd\git.exe' }
$RealGit = Resolve-Executable $RealGit 'git.exe' $gitCandidates
$shimHash = (Get-FileHash -LiteralPath $ShimPath -Algorithm SHA256).Hash
if ($RealGit -ieq $ShimPath -or (Get-FileHash -LiteralPath $RealGit -Algorithm SHA256).Hash -eq $shimHash) {
    throw 'RealGit resolves to the shim. Supply the unchanged Git executable.'
}
$gitVersion = Get-Version $RealGit '^git version\s'
$cowVersion = Get-Version $CowPath '^git-cow-worktree\s'
$shimVersion = Get-Version $ShimPath '^git-cow-shim\s'
if (-not $ShimDirectory) {
    $ShimDirectory = Join-Path $env:LOCALAPPDATA ('git-cow-worktree\' + $traceName + '-shim-' + $shimHash.ToLowerInvariant())
}
$ShimDirectory = [IO.Path]::GetFullPath($ShimDirectory)
$LogDirectory = [IO.Path]::GetFullPath($LogDirectory)
$proxy = Join-Path $ShimDirectory 'git.exe'
foreach ($source in @($RealGit, $ShimPath, $CowPath, $AppPath)) {
    if ($ShimDirectory.TrimEnd('\') -ieq (Split-Path $source -Parent).TrimEnd('\')) {
        throw 'ShimDirectory must be a dedicated directory separate from the source executables.'
    }
}
if (Test-Path -LiteralPath $ShimDirectory) {
    foreach ($entry in (Get-ChildItem -LiteralPath $ShimDirectory -Force)) {
        if ($entry.PSIsContainer -or $entry.Name -ine 'git.exe') { throw "ShimDirectory contains unrelated files: '$ShimDirectory'." }
    }
}
if ((Test-Path -LiteralPath $proxy) -and (Get-FileHash -LiteralPath $proxy -Algorithm SHA256).Hash -ne $shimHash) {
    throw "Existing proxy differs from the selected shim: '$proxy'. Use a new dedicated directory."
}
$running = @(Get-Process -ErrorAction SilentlyContinue | Where-Object {
    # Process paths can use an 8.3 spelling while Get-Item expands AppPath.
    $processPath = $_.Path
    ($processPath -and (Get-Item -LiteralPath $processPath -ErrorAction SilentlyContinue).FullName -ieq $AppPath) -or
        ($appName -ieq 'ChatGPT' -and $_.ProcessName -ieq 'ChatGPT')
})
$summary = [ordered]@{
    App = $AppPath; AppVersion = (Get-Item -LiteralPath $AppPath).VersionInfo.ProductVersion
    PackageVersion = $packageVersion; RunningAppProcesses = $running.Count
    RealGit = $RealGit; GitVersion = $gitVersion; Cow = $CowPath; CowVersion = $cowVersion
    Shim = $ShimPath; ShimVersion = $shimVersion; Proxy = $proxy; LogDirectory = $LogDirectory
    CheckOnly = [bool] $CheckOnly; PrepareOnly = [bool] $PrepareOnly; PassThrough = [bool] $PassThrough
}
if ($CheckOnly) { return [pscustomobject] $summary }
if (-not $PrepareOnly -and $running.Count -gt 0) {
    throw "$appName is already running. Exit it completely, including its tray/background instance, then use the trace shortcut. Existing sessions have been left running."
}

New-Item -ItemType Directory -Path $ShimDirectory -Force | Out-Null
if (-not (Test-Path -LiteralPath $proxy)) { Copy-Item -LiteralPath $ShimPath -Destination $proxy }
$session = Join-Path $LogDirectory ($traceName + '-' + [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $session -Force | Out-Null
$values = @{
    PATH = "$ShimDirectory;$([IO.Path]::GetDirectoryName($CowPath));$env:PATH"
    GIT_COW_REAL_GIT = $RealGit
    GIT_COW_SHIM_TRACE_FILE = (Join-Path $session 'shim.jsonl')
    GIT_TRACE = (Join-Path $session 'git.log')
    GIT_TRACE2_EVENT = (Join-Path $session 'git-events.jsonl')
    GIT_COW_SHIM_TRACE = $null; GIT_COW_SHIM_DEPTH = $null
    GIT_COW_SHIM_BYPASS = $(if ($PassThrough) { '1' } else { $null })
    ELECTRON_RUN_AS_NODE = $null
}

# Check the actual child environment, renamed proxy and both trace writers before
# starting ChatGPT. Parent/user/system environment variables are never modified.
$probeStart = [Diagnostics.ProcessStartInfo]::new()
$probeStart.FileName = $proxy
$probeStart.Arguments = '--version'
$probeStart.UseShellExecute = $false
$probeStart.CreateNoWindow = $true
$probeStart.WindowStyle = [Diagnostics.ProcessWindowStyle]::Hidden
$probeStart.RedirectStandardOutput = $true
$probeStart.RedirectStandardError = $true
Set-ChildEnvironment $probeStart $values
$probe = [Diagnostics.Process]::Start($probeStart)
try {
    if (-not $probe.WaitForExit(30000)) { $probe.Kill(); throw 'Git trace preflight timed out.' }
    $probeOutput = $probe.StandardOutput.ReadToEnd()
    $probeError = $probe.StandardError.ReadToEnd()
    if ($probe.ExitCode -ne 0 -or $probeOutput -notmatch '^git version\s' -or $probeError.Trim()) {
        throw "Git trace preflight failed: $probeOutput $probeError"
    }
} finally { $probe.Dispose() }
foreach ($name in @('GIT_COW_SHIM_TRACE_FILE', 'GIT_TRACE', 'GIT_TRACE2_EVENT')) {
    if (-not (Test-Path -LiteralPath $values[$name] -PathType Leaf) -or (Get-Item -LiteralPath $values[$name]).Length -eq 0) {
        throw "Git preflight did not produce '$($values[$name])'."
    }
}
$summary.Session = $session
$summary.ShimTrace = $values.GIT_COW_SHIM_TRACE_FILE
$summary.GitTrace = $values.GIT_TRACE
$summary.GitEvents = $values.GIT_TRACE2_EVENT
$summary.State = 'prepared'
$summary.StartedAtUtc = [DateTime]::UtcNow.ToString('o')
if (-not $PrepareOnly) {
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $AppPath
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    if ($start.PSObject.Properties.Name -contains 'ArgumentList') {
        foreach ($argument in $AppArguments) { $start.ArgumentList.Add($argument) }
    } else {
        $start.Arguments = (@($AppArguments | ForEach-Object { ConvertTo-NativeArgument $_ })) -join ' '
    }
    Set-ChildEnvironment $start $values
    $process = [Diagnostics.Process]::Start($start)
    $summary.AppProcessId = $process.Id
    $summary.State = 'launched'
    $process.Dispose()
}
$json = ([pscustomobject] $summary) | ConvertTo-Json -Depth 5
$encoding = [Text.UTF8Encoding]::new($false)
[IO.File]::WriteAllText((Join-Path $session 'launch.json'), $json, $encoding)
[IO.File]::WriteAllText((Join-Path $LogDirectory 'latest.json'), $json, $encoding)
return [pscustomobject] $summary
