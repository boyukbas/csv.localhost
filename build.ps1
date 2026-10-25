<#
.SYNOPSIS
    Builds csvlocalhost.exe from source.

.DESCRIPTION
    Requires Go 1.22 or newer on PATH. Produces a single self-contained
    executable next to this script; the browser assets, templates and icon are
    all compiled into it.

.PARAMETER Arch
    Target architecture: amd64 (default) or arm64.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\build.ps1
#>
[CmdletBinding()]
param(
    [ValidateSet('amd64', 'arm64')]
    [string] $Arch = 'amd64'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go was not found on PATH. Install it from https://go.dev/dl/ and try again."
}

Push-Location $PSScriptRoot
try {
    $env:GOOS = 'windows'
    $env:GOARCH = $Arch
    $env:CGO_ENABLED = '0'

    # -s -w strips the symbol table and DWARF data; -H=windowsgui suppresses
    # the console window that would otherwise flash on every double-click.
    Write-Host "Building csvlocalhost.exe ($Arch)..." -ForegroundColor Cyan
    & go build -trimpath -ldflags '-s -w -H=windowsgui' -o csvlocalhost.exe ./cmd/csvlocalhost
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }

    $size = [math]::Round((Get-Item csvlocalhost.exe).Length / 1MB, 1)
    Write-Host "Built csvlocalhost.exe ($size MB)" -ForegroundColor Green
    Write-Host "Next: powershell -ExecutionPolicy Bypass -File .\install.ps1" -ForegroundColor DarkGray
} finally {
    Pop-Location
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}
