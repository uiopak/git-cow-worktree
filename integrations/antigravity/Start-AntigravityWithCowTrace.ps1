# Opt-in Git routing and tracing for Antigravity's Windows app and standalone IDE.
[CmdletBinding()]
param(
    [ValidateSet('App', 'IDE')][string] $Application = 'App',
    [string] $AppPath,
    [string[]] $AppArguments = @(),
    [string] $ShortcutPath,
    [string] $RealGit,
    [string] $CowPath,
    [string] $ShimPath,
    [string] $ShimDirectory,
    [string] $LogDirectory,
    [switch] $CheckOnly,
    [switch] $PrepareOnly,
    [switch] $PassThrough
)

$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'This launcher requires Windows.' }
if ($CheckOnly -and $PrepareOnly) { throw 'Choose either CheckOnly or PrepareOnly.' }
$displayName = if ($Application -eq 'IDE') { 'Antigravity IDE' } else { 'Antigravity' }

function Get-ShortcutExecutable([string] $Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    $shell = New-Object -ComObject WScript.Shell
    $link = $shell.CreateShortcut($Path)
    if ($link.Arguments) {
        throw "Shortcut has arguments. Supply AppPath and AppArguments explicitly: '$Path'."
    }
    if ($link.TargetPath -and (Test-Path -LiteralPath $link.TargetPath -PathType Leaf)) {
        return $link.TargetPath
    }

    # An AppsFolder shortcut can contain an AppUserModelID instead of an exe.
    # Resolve the registered application, then launch its exe with a child-only
    # environment. Shell activation could reuse a process with the ordinary PATH.
    $shellApp = New-Object -ComObject Shell.Application
    $item = $shellApp.Namespace((Split-Path $Path -Parent)).ParseName((Split-Path $Path -Leaf))
    $target = $item.ExtendedProperty('System.Link.TargetParsingPath')
    if ($target -and (Test-Path -LiteralPath $target -PathType Leaf)) { return $target }
    if ($target) {
        foreach ($app in $shellApp.Namespace('shell:AppsFolder').Items()) {
            if ($app.ExtendedProperty('System.AppUserModel.ID') -ne $target) { continue }
            $executable = $app.ExtendedProperty('System.Link.TargetParsingPath')
            if ($executable -and (Test-Path -LiteralPath $executable -PathType Leaf)) { return $executable }
        }
    }
}

if (-not $AppPath) {
    if ($ShortcutPath) {
        $ShortcutPath = [IO.Path]::GetFullPath($ShortcutPath)
        $AppPath = Get-ShortcutExecutable $ShortcutPath
        if (-not $AppPath) { throw "Cannot resolve the shortcut's executable: '$ShortcutPath'. Supply AppPath explicitly." }
    } else {
        $desktopShortcut = Join-Path ([Environment]::GetFolderPath('Desktop')) ($displayName + '.lnk')
        $AppPath = Get-ShortcutExecutable $desktopShortcut
        if (-not $AppPath) {
            $candidates = @()
            if ($env:LOCALAPPDATA) { $candidates += Join-Path $env:LOCALAPPDATA "Programs\$displayName\$displayName.exe" }
            if ($env:ProgramFiles) { $candidates += Join-Path $env:ProgramFiles "$displayName\$displayName.exe" }
            foreach ($candidate in $candidates) {
                if (Test-Path -LiteralPath $candidate -PathType Leaf) { $AppPath = $candidate; break }
            }
        }
    }
}
if (-not $AppPath) { throw "Cannot discover $displayName. Supply AppPath explicitly." }
$appItem = Get-Item -LiteralPath $AppPath
if ($appItem.PSIsContainer -or $appItem.Extension -ine '.exe') { throw "Expected a native executable: '$AppPath'." }
$AppPath = $appItem.FullName

# A persistent language-server process may keep its old environment after the
# GUI exits. Report it rather than terminating existing work or trusting PATH
# inheritance from a new window.
$appDirectory = Split-Path $AppPath -Parent
$backendPaths = @(
    (Join-Path $appDirectory 'resources\bin\language_server.exe'),
    (Join-Path $appDirectory 'resources\app\extensions\antigravity\bin\language_server_windows_x64.exe'),
    (Join-Path $appDirectory 'resources\app\extensions\antigravity\bin\language_server_windows_arm.exe'),
    (Join-Path $appDirectory 'resources\app\extensions\antigravity\bin\language_server_windows_arm64.exe')
)
$runningBackends = @(Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -and $backendPaths -icontains $_.Path })
if (-not $CheckOnly -and -not $PrepareOnly -and $runningBackends.Count -gt 0) {
    throw "$displayName's language server is already running. Finish your sessions and exit the app completely before using the trace shortcut. Existing processes have been left running."
}
if (-not $LogDirectory) {
    $traceName = if ($Application -eq 'IDE') { 'antigravity-ide-traces' } else { 'antigravity-traces' }
    $LogDirectory = Join-Path $env:LOCALAPPDATA ('git-cow-worktree\' + $traceName)
}

# Reuse the existing trace launcher with an explicit executable. Its automatic
# ChatGPT package discovery is skipped; proxy preflight, Windows argv quoting,
# recursion protection and trace writing are shared by both integrations.
$arguments = @{
    AppPath = $AppPath; AppArguments = $AppArguments; LogDirectory = $LogDirectory
    CheckOnly = $CheckOnly; PrepareOnly = $PrepareOnly; PassThrough = $PassThrough
}
foreach ($name in @('RealGit', 'CowPath', 'ShimPath', 'ShimDirectory')) {
    $value = Get-Variable -Name $name -ValueOnly
    if ($value) { $arguments[$name] = $value }
}
$launcher = Join-Path (Split-Path $PSScriptRoot -Parent) 'codex\Start-ChatGPTWithCowTrace.ps1'
$summary = & $launcher @arguments
if ($CheckOnly) {
    $summary | Add-Member -NotePropertyName RunningBackendProcesses -NotePropertyValue $runningBackends.Count
}
return $summary
