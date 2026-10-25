<#
.SYNOPSIS
    Removes csv.localhost and its file associations.

.DESCRIPTION
    Stops a running server, deletes the registry keys created by install.ps1
    and removes the installed binary. Everything it touches lives under
    HKEY_CURRENT_USER and your own profile.

.PARAMETER InstallDir
    Where the binary was installed. Defaults to
    %LOCALAPPDATA%\Programs\csv.localhost.

.PARAMETER KeepFiles
    Remove the associations but leave the installed binary in place.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\uninstall.ps1
#>
[CmdletBinding()]
param(
    [string]   $InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\csv.localhost'),
    [string[]] $Extensions = @('.csv', '.tsv', '.tab'),
    [switch]   $KeepFiles
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ProgId  = 'csvlocalhost.delimited'
$classes = 'HKCU:\Software\Classes'

function Write-Step { param([string]$Text) Write-Host "  $Text" -ForegroundColor Cyan }

Write-Host ""
Write-Host "csv.localhost uninstaller" -ForegroundColor White
Write-Host ""

# Stop the background server so nothing holds the executable open.
$exe = Join-Path $InstallDir 'csvlocalhost.exe'
foreach ($candidate in @($exe, (Join-Path $PSScriptRoot 'csvlocalhost.exe'))) {
    if (Test-Path $candidate) {
        try { & $candidate stop 2>$null | Out-Null } catch { }
    }
}
Get-Process -Name 'csvlocalhost' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

foreach ($ext in $Extensions) {
    $progIds = "$classes\$ext\OpenWithProgids"
    if (Test-Path $progIds) {
        Remove-ItemProperty -Path $progIds -Name $ProgId -ErrorAction SilentlyContinue
    }
    $extKey = "$classes\$ext"
    if (Test-Path $extKey) {
        $default = (Get-Item $extKey).GetValue('')
        if ($default -eq $ProgId) {
            # Clear our claim but leave the key: other handlers may list here.
            Set-ItemProperty -Path $extKey -Name '(Default)' -Value ''
        }
    }
}
Write-Step ("Released " + ($Extensions -join ', '))

foreach ($key in @("$classes\$ProgId", "$classes\Applications\csvlocalhost.exe")) {
    if (Test-Path $key) { Remove-Item -Path $key -Recurse -Force }
}
Write-Step "Removed file type registration"

$lnk = Join-Path ([Environment]::GetFolderPath('Programs')) 'csv.localhost.lnk'
if (Test-Path $lnk) { Remove-Item $lnk -Force; Write-Step "Removed Start Menu shortcut" }

$stateDir = Join-Path $env:LOCALAPPDATA 'csv.localhost'
if (Test-Path $stateDir) { Remove-Item $stateDir -Recurse -Force; Write-Step "Removed cached state" }

if (-not $KeepFiles -and (Test-Path $InstallDir)) {
    try {
        Remove-Item $InstallDir -Recurse -Force
        Write-Step "Removed $InstallDir"
    } catch {
        Write-Warning "Could not delete $InstallDir - it may still be in use. Delete it manually."
    }
}

if (-not ('CsvLocalhost.Shell32Un' -as [type])) {
    Add-Type -Namespace CsvLocalhost -Name Shell32Un -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("shell32.dll")]
public static extern void SHChangeNotify(int eventId, uint flags, System.IntPtr item1, System.IntPtr item2);
'@
}
[CsvLocalhost.Shell32Un]::SHChangeNotify(0x08000000, 0x0000, [IntPtr]::Zero, [IntPtr]::Zero)

Write-Host ""
Write-Host "Done." -ForegroundColor Green
Write-Host "  If Windows still shows csv.localhost as the default for .csv, pick a new" -ForegroundColor DarkGray
Write-Host "  default via right-click -> Open with -> Choose another app." -ForegroundColor DarkGray
Write-Host ""
