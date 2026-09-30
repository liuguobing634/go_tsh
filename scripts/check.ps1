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
    [string]$CacheDir = '',

    # Optional module proxy, e.g. https://goproxy.cn,direct. Empty leaves
    # GOPROXY untouched. Needed only when new modules must be downloaded.
    [string]$GoProxy = ''
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

# Redirect a Go cache directory into the workspace when the default location is
# not writable. A workspace-write sandbox only allows writes under the repo, so
# both the build cache and the module cache need this treatment.
function Set-GoCache {
    param([string]$Name, [string]$Fallback)

    if ($CacheDir -and $Name -eq 'GOCACHE') {
        New-Item -ItemType Directory -Path $CacheDir -Force | Out-Null
        [Environment]::SetEnvironmentVariable($Name, $CacheDir)
        Write-Host "$Name -> $CacheDir (forced)" -ForegroundColor DarkGray
        return
    }

    if ([Environment]::GetEnvironmentVariable($Name)) {
        Write-Host "$Name -> $([Environment]::GetEnvironmentVariable($Name))" -ForegroundColor DarkGray
        return
    }

    $default = (& $GoExe env $Name 2>&1 | Out-String).Trim()
    if ($default -and (Test-UsableDirectory $default)) {
        Write-Host "$Name -> $default" -ForegroundColor DarkGray
        return
    }

    New-Item -ItemType Directory -Path $Fallback -Force | Out-Null
    [Environment]::SetEnvironmentVariable($Name, $Fallback)
    Write-Host "$Name -> $Fallback" -ForegroundColor Yellow
    Write-Host "    (default $Name is not writable here, likely a filesystem sandbox)" -ForegroundColor Yellow
}

Set-GoCache -Name 'GOCACHE' -Fallback (Join-Path $repoRoot '.gocache')

# The module cache needs the same treatment: with third-party deps in play the
# build reads it, and the default lives outside the repo (%GOPATH%\pkg\mod).
Set-GoCache -Name 'GOMODCACHE' -Fallback (Join-Path $repoRoot '.gocache\mod')

# Pass -GoProxy when new modules must be downloaded, e.g.
#   scripts/check.ps1 -GoProxy https://goproxy.cn,direct
# proxy.golang.org is unreachable from this network; the mirrors work.
# GOSUMDB is disabled alongside it because sumdb writes to %GOPATH%\pkg\sumdb,
# which a workspace-write sandbox rejects.
if ($GoProxy) {
    [Environment]::SetEnvironmentVariable('GOPROXY', $GoProxy)
    [Environment]::SetEnvironmentVariable('GOSUMDB', 'off')
    Write-Host "GOPROXY -> $GoProxy" -ForegroundColor DarkGray
}

$goroot = (& $GoExe env GOROOT | Out-String).Trim()
$gofmt = Join-Path $goroot 'bin\gofmt.exe'
if (-not (Test-Path $gofmt)) {
    $gofmt = Join-Path $goroot 'bin/gofmt'
}

# gofmt -l exits 0 even when it lists unformatted files, so its output must be
# inspected instead of its exit code.
#
# Do NOT run `gofmt -l .`: the module cache now lives inside the repo
# (.gocache/mod) and gofmt does not skip dot-directories, so it would report
# third-party sources as our formatting failures. Ask Go for this module's own
# package directories instead.
Write-Host '==> gofmt -l (formatting)' -ForegroundColor Cyan
$pkgDirs = @(& $GoExe list -f '{{.Dir}}' ./... 2>$null | Where-Object { $_ })
if ($pkgDirs.Count -eq 0) {
    Write-Host '    FAILED (go list returned no package directories)' -ForegroundColor Red
    $global:failed += 'gofmt'
} else {
    $unformatted = & $gofmt -l $pkgDirs
    if ($unformatted) {
        Write-Host '    not formatted:' -ForegroundColor Red
        $unformatted | ForEach-Object { Write-Host "      $_" }
        $global:failed += 'gofmt'
    }
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
