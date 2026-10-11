$ErrorActionPreference = 'Stop'

function Get-SmokeExecutable([string] $Value, [string] $Name) {
    if (-not $Value) {
        if ($Name -eq 'git.exe' -and (Test-Path -LiteralPath "$env:ProgramFiles\Git\cmd\git.exe")) {
            $Value = "$env:ProgramFiles\Git\cmd\git.exe"
        } else {
            $Value = (Get-Command $Name -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
        }
    }
    $item = Get-Item -LiteralPath $Value
    if ($item.PSIsContainer -or $item.Extension -ine '.exe') { throw "Expected an executable: $Value" }
    return $item.FullName
}

function Invoke-SmokeGit([string] $Executable, [string[]] $Arguments) {
    $output = @(& $Executable @Arguments)
    if ($LASTEXITCODE -ne 0) { throw "Git exited $LASTEXITCODE for: $($Arguments -join ' ')" }
    return $output
}

function Write-SmokeText([string] $Path, [string] $Text) {
    [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function Read-SmokeManifest([string] $Root) {
    $manifest = Get-Content -LiteralPath (Join-Path $Root 'manifest.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($manifest.kind -ne 'git-cow-worktree-smoke-v1' -or
        [IO.Path]::GetFullPath($Root).TrimEnd('\') -ine $manifest.root.TrimEnd('\')) {
        throw 'This directory does not contain the expected smoke fixture.'
    }
    return $manifest
}

function Initialize-ExtentProbe {
    if (-not ('CowSmoke.ExtentProbe' -as [type])) {
        Add-Type -Path (Join-Path $PSScriptRoot 'ExtentProbe.cs')
    }
}
