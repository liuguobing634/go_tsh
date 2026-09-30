#!/usr/bin/env pwsh
<#
.SYNOPSIS
    go_tsh 本地开发门禁：格式 + 静态检查 + 竞态测试。

.DESCRIPTION
    等价于 Makefile 的 `make check`，但可在没有 make 的 Windows 上直接运行。
    它会自动从 GOROOT 推导 gofmt 的绝对路径，因此即使 go 没有加入 PATH 也能用。

.EXAMPLE
    pwsh -File scripts/check.ps1

.EXAMPLE
    pwsh -File scripts/check.ps1 -GoExe "D:\Program Files\Go\bin\go.exe"
#>
[CmdletBinding()]
param(
    # go 可执行文件；不在 PATH 上时传绝对路径。
    [string]$GoExe = 'go',

    # 要检查的包模式。
    [string]$Package = './...'
)

$ErrorActionPreference = 'Continue'
# 避免 PowerShell 7.4+ 把原生命令写入 stderr 当成终止性错误。
if (Test-Path Variable:PSNativeCommandUseErrorActionPreference) {
    $PSNativeCommandUseErrorActionPreference = $false
}

$failed = @()

function Test-Step {
    param([string]$Name, [scriptblock]$Action)

    Write-Host "==> $Name" -ForegroundColor Cyan
    $ok = & $Action
    if (-not $ok) {
        Write-Host "    失败" -ForegroundColor Red
        $global:failed += $Name
    }
}

# 先确认 go 可用，否则后续报错信息会很难懂。
try {
    $goVersion = (& $GoExe version 2>&1).Trim()
} catch {
    $goVersion = $null
}
if (-not $goVersion -or $LASTEXITCODE -ne 0) {
    Write-Host "找不到可用的 Go：'$GoExe'。请用 -GoExe 指定绝对路径。" -ForegroundColor Red
    exit 1
}
Write-Host "使用 $goVersion" -ForegroundColor DarkGray

$goroot = (& $GoExe env GOROOT).Trim()
$gofmt = Join-Path $goroot 'bin\gofmt.exe'
if (-not (Test-Path $gofmt)) {
    $gofmt = Join-Path $goroot 'bin/gofmt'
}

Test-Step 'gofmt -l（格式校验）' {
    $unformatted = & $gofmt -l .
    if ($unformatted) {
        Write-Host '    以下文件未格式化：' -ForegroundColor Red
        $unformatted | ForEach-Object { Write-Host "      $_" }
        return $false
    }
    return $true
}

Test-Step 'go vet' {
    & $GoExe vet $Package
    return ($LASTEXITCODE -eq 0)
}

# -race 需要 cgo 与 C 编译器；缺失时退回普通测试，避免门禁无故红掉。
$cgo = (& $GoExe env CGO_ENABLED).Trim()
$hasCC = $null -ne (Get-Command gcc -ErrorAction SilentlyContinue)
if ($cgo -eq '1' -and $hasCC) {
    Test-Step 'go test -race' {
        & $GoExe test -race $Package
        return ($LASTEXITCODE -eq 0)
    }
} else {
    Write-Host '    跳过 -race（需要 cgo + C 编译器），退回普通 go test' -ForegroundColor Yellow
    Test-Step 'go test' {
        & $GoExe test $Package
        return ($LASTEXITCODE -eq 0)
    }
}

if ($failed.Count -eq 0) {
    Write-Host 'check 全部通过' -ForegroundColor Green
    exit 0
}

Write-Host ('check 失败: ' + ($failed -join '、')) -ForegroundColor Red
exit 1
