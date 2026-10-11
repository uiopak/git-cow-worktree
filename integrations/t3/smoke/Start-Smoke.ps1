[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $FixtureRoot,
    [string] $T3Path,
    [switch] $CheckOnly,
    [switch] $Trace
)
. (Join-Path $PSScriptRoot 'Common.ps1')
$manifest = Read-SmokeManifest ([IO.Path]::GetFullPath($FixtureRoot))
if (-not $T3Path) { $T3Path = $manifest.t3 }
if (-not $T3Path) { throw 'Supply -T3Path with the native T3 executable path.' }
$T3Path = Get-SmokeExecutable $T3Path 'T3 Code.exe'
if (-not $CheckOnly) {
    $running = @(Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -ieq $T3Path })
    if ($running.Count) { throw 'T3 is already running. Exit T3 completely, then run this launcher so the new process inherits the shim environment.' }
}
$launcher = Join-Path $PSScriptRoot 'Start-T3WithCow.ps1'
if (-not (Test-Path -LiteralPath $launcher)) { $launcher = Join-Path (Split-Path $PSScriptRoot -Parent) 'Start-T3WithCow.ps1' }
& $launcher -RealGit $manifest.realGit -CowPath $manifest.cow -ShimPath $manifest.shim `
    -T3Path $T3Path -ShimDirectory (Join-Path $manifest.root 'shim') `
    -TraceFile $manifest.traceFile -CheckOnly:$CheckOnly -Trace:$Trace
