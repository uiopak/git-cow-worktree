# Set metadata on an existing COW shortcut. Pinning stays a manual user action.
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [Parameter(Mandatory = $true)][string] $ShortcutPath,
    [Parameter(Mandatory = $true)][string] $AppId
)

$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'This script requires Windows.' }
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Utility') -Scope Local -ErrorAction Stop
$shortcut = Get-Item -LiteralPath $ShortcutPath
if ($shortcut.PSIsContainer -or $shortcut.Extension -ine '.lnk') { throw 'Expected an existing .lnk shortcut.' }
if (-not $PSCmdlet.ShouldProcess($shortcut.FullName, "Set taskbar app ID to '$AppId'")) { return }

if (-not ('CowShortcutIdentity' -as [type])) {
    Add-Type -Path (Join-Path $PSScriptRoot 'ShortcutIdentity.cs')
}
[CowShortcutIdentity]::Set($shortcut.FullName, $AppId)
$saved = [CowShortcutIdentity]::Read($shortcut.FullName)
if ($saved -cne $AppId) { throw 'Shortcut app ID verification failed.' }
[pscustomobject]@{ Shortcut = $shortcut.FullName; AppId = $saved }
