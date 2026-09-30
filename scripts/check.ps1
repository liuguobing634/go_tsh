#!/usr/bin/env pwsh
<#
.SYNOPSIS
    go_tsh local development gate: formatting + vet + race test.

.DESCRIPTION
    Equivalent to `make check`, but runs on Windows without make.
    Derives the absolute path of gofmt from GOROOT, so it works even when
    'go' is not on PATH.

    Also adapts GOCACHE: a workspace-write filesystem sandbox rejects writes
    to the default cache location (C:\Users\<user>\AppData\Local\go-build)
    because it lives outside the repository. When the default cache is not
    writable, the build cache is redirected into <repo>/.gocache instead of
    failing the gate. Outside a sandbox nothing changes.

    NOTE: This file is intentionally ASCII-only.
    Windows PowerShell 5.1 reads BOM-less .ps1 files as ANSI (GBK on zh-CN
    systems). Non-ASCII characters then get corrupted and can break the
    parser outright. Keep this script ASCII, or save it as UTF-8 *with BOM*.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/check.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/check.ps1 -GoExe "D:\Program Files\Go\bin\go.exe"
#>
[CmdletBinding()]
param(
    # go executable. Pass an absolute path when it is not on PATH.
    [string]$GoExe = 'go',

    # Package pattern to check.
    [string]$Package = './...',

    # Force the Go build cache to this directory. Empty means auto-detect.
    [string]$CacheDir = ''
)

$ErrorActionPreference = 'Continue'
# Keep native stderr writes from becoming terminating errors on PS 7.4+.
if (Test-Path Variable:PSNativeCommandUseErrorActionPreference) {
    $PSNativeCommandUseErrorActionPreference = $false
}

$failed = @()

# Runs a check and reports failure from the native exit code.
#
# Do NOT decide pass/fail by capturing the scriptblock's output: a native
# command's stdout lands in that same pipeline, so a non-empty array looks
# truthy and the gate would silently pass everything. That bug shipped once.
function Invoke-Checked {
    param([string]$Name, [scriptblock]$Action)

    Write-Host "==> $Name" -ForegroundColor Cyan
    $global:LASTEXITCODE = 0
    & $Action
    if ($LASTEXITCODE -ne 0) {
        Write-Host "    FAILED (exit $LASTEXITCODE)" -ForegroundColor Red
        $global:failed += $Name
    }
}

# Report whether a directory can actually be written to. Creating the probe
# file is the only reliable test: ACLs, restricted tokens and read-only
# mounts all look identical to Test-Path alone.
function Test-UsableDirectory {
    param([string]$Path)

    try {
        if (-not (Test-Path $Path)) {
            New-Item -ItemType Directory -Path $Path -Force | Out-Null
        }
        $probe = Join-Path $Path ('.write-probe-' + [guid]::NewGuid().ToString('N'))
        [System.IO.File]::WriteAllText($probe, 'probe')
        Remove-Item $probe -Force
        return $true
    } catch {
        return $false
    }
}

# Locate a C compiler for the race detector.
#
# On Windows the usual source is MSYS2, whose toolchain lives outside the
# default PATH (the MSYS2 shell sets it up itself). Probe for it explicitly
# rather than depending on the user having configured PATH correctly.
#
# Note: only the toolchain bin dir is used. C:\msys64\usr\bin must never go on
# the Windows PATH - it ships MSYS2's own link.exe / find.exe / sort.exe / sh.exe
# which shadow the native tools and break unrelated builds.
function Find-CCompiler {
    $cmd = Get-Command gcc -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    foreach ($root in @('C:\msys64', 'D:\msys64', 'C:\msys32', 'D:\msys32')) {
        foreach ($variant in 'ucrt64', 'mingw64', 'clang64', 'mingw32') {
            $exe = Join-Path $root "$variant\bin\gcc.exe"
            if (Test-Path $exe) { return $exe }
        }
    }
    return $null
}

# Fail fast with a readable message when go itself is missing.
$goVersion = $null
try { $goVersion = (& $GoExe version 2>&1 | Out-String).Trim() } catch { $goVersion = $null }
if (-not $goVersion -or $LASTEXITCODE -ne 0) {
    Write-Host "Cannot run Go ('$GoExe'). Use -GoExe to pass an absolute path." -ForegroundColor Red
    exit 1
}
Write-Host "using $goVersion" -ForegroundColor DarkGray

$repoRoot = Split-Path -Parent $PSScriptRoot

if ($CacheDir) {
    New-Item -ItemType Directory -Path $CacheDir -Force | Out-Null
    $env:GOCACHE = $CacheDir
    Write-Host "GOCACHE -> $CacheDir (forced)" -ForegroundColor DarkGray
} elseif (-not $env:GOCACHE) {
    $default = (& $GoExe env GOCACHE 2>&1 | Out-String).Trim()
    if (Test-UsableDirectory $default) {
        Write-Host "GOCACHE -> $default" -ForegroundColor DarkGray
    } else {
        $fallback = Join-Path $repoRoot '.gocache'
        New-Item -ItemType Directory -Path $fallback -Force | Out-Null
        $env:GOCACHE = $fallback
        Write-Host "GOCACHE -> $fallback" -ForegroundColor Yellow
        Write-Host '    (default cache is not writable here, likely a filesystem sandbox)' -ForegroundColor Yellow
    }
}

$goroot = (& $GoExe env GOROOT | Out-String).Trim()
$gofmt = Join-Path $goroot 'bin\gofmt.exe'
if (-not (Test-Path $gofmt)) {
    $gofmt = Join-Path $goroot 'bin/gofmt'
}

# gofmt -l exits 0 even when it lists unformatted files, so its output must be
# inspected instead of its exit code.
Write-Host '==> gofmt -l (formatting)' -ForegroundColor Cyan
$unformatted = & $gofmt -l .
if ($unformatted) {
    Write-Host '    not formatted:' -ForegroundColor Red
    $unformatted | ForEach-Object { Write-Host "      $_" }
    $global:failed += 'gofmt'
}

Invoke-Checked 'go vet' { & $GoExe vet $Package }

# The race detector needs cgo and a C compiler. Degrade gracefully instead of
# failing the gate for a purely environmental reason.
$cc = Find-CCompiler
if ($cc) {
    $ccDir = Split-Path -Parent $cc
    if (($env:PATH -split ';') -notcontains $ccDir) {
        $env:PATH = $ccDir + ';' + $env:PATH
        Write-Host "CC -> $cc" -ForegroundColor DarkGray
    }
    $env:CGO_ENABLED = '1'
}

$cgo = (& $GoExe env CGO_ENABLED | Out-String).Trim()
$hasCC = $null -ne (Get-Command gcc -ErrorAction SilentlyContinue)
if ($cgo -eq '1' -and $hasCC) {
    Invoke-Checked 'go test -race' { & $GoExe test -race $Package }
} else {
    Write-Host '    SKIP -race (needs cgo + a C compiler), falling back to go test' -ForegroundColor Yellow
    Invoke-Checked 'go test' { & $GoExe test $Package }
}

if ($failed.Count -eq 0) {
    Write-Host 'check passed' -ForegroundColor Green
    exit 0
}

Write-Host ('check FAILED: ' + ($failed -join ', ')) -ForegroundColor Red
exit 1
