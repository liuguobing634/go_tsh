#!/usr/bin/env pwsh
<#
.SYNOPSIS
    go_tsh local development gate: formatting + vet + race test.

.DESCRIPTION
    Equivalent to `make check`, but runs on Windows without make.
    Derives the absolute path of gofmt from GOROOT, so it works even when
    'go' is not on PATH.

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
    [string]$Package = './...'
)

$ErrorActionPreference = 'Continue'
# Keep native stderr writes from becoming terminating errors on PS 7.4+.
if (Test-Path Variable:PSNativeCommandUseErrorActionPreference) {
    $PSNativeCommandUseErrorActionPreference = $false
}

$failed = @()

function Test-Step {
    param([string]$Name, [scriptblock]$Action)

    Write-Host "==> $Name" -ForegroundColor Cyan
    $ok = & $Action
    if (-not $ok) {
        Write-Host '    FAILED' -ForegroundColor Red
        $global:failed += $Name
    }
}

# Fail fast with a readable message when go itself is missing.
$goVersion = $null
try { $goVersion = (& $GoExe version 2>&1 | Out-String).Trim() } catch { $goVersion = $null }
if (-not $goVersion -or $LASTEXITCODE -ne 0) {
    Write-Host "Cannot run Go ('$GoExe'). Use -GoExe to pass an absolute path." -ForegroundColor Red
    exit 1
}
Write-Host "using $goVersion" -ForegroundColor DarkGray

$goroot = (& $GoExe env GOROOT | Out-String).Trim()
$gofmt = Join-Path $goroot 'bin\gofmt.exe'
if (-not (Test-Path $gofmt)) {
    $gofmt = Join-Path $goroot 'bin/gofmt'
}

Test-Step 'gofmt -l (formatting)' {
    $unformatted = & $gofmt -l .
    if ($unformatted) {
        Write-Host '    not formatted:' -ForegroundColor Red
        $unformatted | ForEach-Object { Write-Host "      $_" }
        return $false
    }
    return $true
}

Test-Step 'go vet' {
    & $GoExe vet $Package
    return ($LASTEXITCODE -eq 0)
}

# The race detector needs cgo and a C compiler. Degrade gracefully instead of
# failing the gate for a purely environmental reason.
$cgo = (& $GoExe env CGO_ENABLED | Out-String).Trim()
$hasCC = $null -ne (Get-Command gcc -ErrorAction SilentlyContinue)
if ($cgo -eq '1' -and $hasCC) {
    Test-Step 'go test -race' {
        & $GoExe test -race $Package
        return ($LASTEXITCODE -eq 0)
    }
} else {
    Write-Host '    SKIP -race (needs cgo + a C compiler), falling back to go test' -ForegroundColor Yellow
    Test-Step 'go test' {
        & $GoExe test $Package
        return ($LASTEXITCODE -eq 0)
    }
}

if ($failed.Count -eq 0) {
    Write-Host 'check passed' -ForegroundColor Green
    exit 0
}

Write-Host ('check FAILED: ' + ($failed -join ', ')) -ForegroundColor Red
exit 1
