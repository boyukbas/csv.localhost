<#
.SYNOPSIS
    Installs csv.localhost and makes Windows open .csv files in your browser.

.DESCRIPTION
    Copies csvlocalhost.exe into your per-user programs folder, registers a
    file type for delimited text, and points .csv, .tsv and .tab at it. An
    "Open in Excel" entry is added to the right-click menu so the spreadsheet
    is still one click away. Everything is written under HKEY_CURRENT_USER, so
    no administrator rights are needed and nothing outside your profile is
    touched.

.PARAMETER InstallDir
    Where to place the binary. Defaults to
    %LOCALAPPDATA%\Programs\csv.localhost.

.PARAMETER InPlace
    Register the executable where it already sits instead of copying it.
    Handy while developing; do not move the folder afterwards.

.PARAMETER Extensions
    Which extensions to claim. Defaults to .csv, .tsv, .tab.

.PARAMETER StartMenu
    Add a Start Menu shortcut that opens the "recently viewed" page.

.PARAMETER NoExcelVerb
    Skip the "Open in Excel" right-click entry.

.PARAMETER HostsEntry
    Also add "127.0.0.1 csv.localhost" to the system hosts file. Only needed
    for browsers that do not resolve *.localhost themselves; requires an
    elevated PowerShell session.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\install.ps1 -StartMenu
#>
[CmdletBinding()]
param(
    [string]   $InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\csv.localhost'),
    [switch]   $InPlace,
    [string[]] $Extensions = @('.csv', '.tsv', '.tab'),
    [switch]   $StartMenu,
    [switch]   $NoExcelVerb,
    [switch]   $HostsEntry
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ProgId    = 'csvlocalhost.delimited'
$AppName   = 'csv.localhost'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

function Write-Step { param([string]$Text) Write-Host "  $Text" -ForegroundColor Cyan }
function Write-Note { param([string]$Text) Write-Host "  $Text" -ForegroundColor DarkGray }

Write-Host ""
Write-Host "csv.localhost installer" -ForegroundColor White
Write-Host ""

# ---------------------------------------------------------------- binary

$sourceExe = Join-Path $ScriptDir 'csvlocalhost.exe'
$sourceIco = Join-Path $ScriptDir 'csv.ico'
if (-not (Test-Path $sourceExe)) {
    throw "csvlocalhost.exe was not found next to this script ($ScriptDir). Build it first with build.ps1, or download a release."
}

if ($InPlace) {
    $exe = (Resolve-Path $sourceExe).Path
    $ico = if (Test-Path $sourceIco) { (Resolve-Path $sourceIco).Path } else { $exe }
    Write-Step "Registering in place: $exe"
} else {
    if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }
    $exe = Join-Path $InstallDir 'csvlocalhost.exe'
    $ico = Join-Path $InstallDir 'csv.ico'

    # A running server holds the file open; ask it to stop before overwriting.
    if (Test-Path $exe) {
        try { & $exe stop 2>$null | Out-Null; Start-Sleep -Milliseconds 600 } catch { }
    }
    Copy-Item $sourceExe $exe -Force
    if (Test-Path $sourceIco) { Copy-Item $sourceIco $ico -Force } else { $ico = $exe }
    Write-Step "Installed to $InstallDir"
}

# ---------------------------------------------------------------- prog id

function Set-Default {
    param([string]$Path, [string]$Value)
    if (-not (Test-Path $Path)) { New-Item -Path $Path -Force | Out-Null }
    Set-ItemProperty -Path $Path -Name '(Default)' -Value $Value
}

$classes = 'HKCU:\Software\Classes'

Set-Default "$classes\$ProgId"                    'Delimited Text (CSV)'
Set-Default "$classes\$ProgId\DefaultIcon"        "`"$ico`",0"
Set-Default "$classes\$ProgId\shell\open"         '&View in browser'
Set-Default "$classes\$ProgId\shell\open\command" "`"$exe`" open `"%1`""
Set-ItemProperty -Path "$classes\$ProgId" -Name 'FriendlyTypeName' -Value 'Delimited Text (CSV)'
Set-ItemProperty -Path "$classes\$ProgId" -Name 'AlwaysShowExt' -Value ''

# Excel stays one right-click away. Its location comes from the App Paths key
# rather than a guessed Program Files path, so Click-to-Run installs work too.
$excel = $null
if (-not $NoExcelVerb) {
    foreach ($hive in @('HKLM:', 'HKCU:')) {
        $key = "$hive\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\excel.exe"
        if (Test-Path $key) {
            $candidate = (Get-Item $key).GetValue('')
            if ($candidate -and (Test-Path $candidate)) { $excel = $candidate; break }
        }
    }
}
if ($excel) {
    Set-Default "$classes\$ProgId\shell\excel"         'Open in &Excel'
    Set-Default "$classes\$ProgId\shell\excel\command" "`"$excel`" `"%1`""
    Set-ItemProperty -Path "$classes\$ProgId\shell\excel" -Name 'Icon' -Value "`"$excel`",0"
    Write-Step "Added an 'Open in Excel' right-click entry"
} elseif (-not $NoExcelVerb) {
    Write-Note "Excel was not found in the registry; skipping that right-click entry."
}

# A third verb, so the raw text is still reachable.
Set-Default "$classes\$ProgId\shell\edit"         'Edit in &Notepad'
Set-Default "$classes\$ProgId\shell\edit\command" "notepad.exe `"%1`""

# Make the app appear under "Open with" even where it is not the default.
$appKey = "$classes\Applications\csvlocalhost.exe"
Set-Default "$appKey\shell\open\command" "`"$exe`" open `"%1`""
Set-ItemProperty -Path $appKey -Name 'FriendlyAppName' -Value $AppName
if (-not (Test-Path "$appKey\SupportedTypes")) { New-Item -Path "$appKey\SupportedTypes" -Force | Out-Null }
foreach ($ext in $Extensions) {
    Set-ItemProperty -Path "$appKey\SupportedTypes" -Name $ext -Value ''
}

Write-Step "Registered file type '$ProgId'"

# ---------------------------------------------------------------- extensions

$needsManual = @()

foreach ($ext in $Extensions) {
    $extKey = "$classes\$ext"
    if (-not (Test-Path $extKey)) { New-Item -Path $extKey -Force | Out-Null }

    $progIds = "$extKey\OpenWithProgids"
    if (-not (Test-Path $progIds)) { New-Item -Path $progIds -Force | Out-Null }
    # Explorer only cares that the value *name* exists; an empty string is
    # accepted everywhere, while an empty REG_NONE trips some PowerShell hosts.
    Set-ItemProperty -Path $progIds -Name $ProgId -Value '' -Type String

    # Windows 10/11 protect the *default* handler with a signed UserChoice key
    # that applications are not permitted to write. Claim the extension only
    # when the user has not already chosen something.
    $userChoice = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\$ext\UserChoice"
    if (Test-Path $userChoice) {
        $choice  = Get-ItemProperty -Path $userChoice -ErrorAction SilentlyContinue
        $current = if ($choice -and $choice.PSObject.Properties['ProgId']) { $choice.ProgId } else { $null }
        if ($current -ne $ProgId) { $needsManual += $ext }
    } else {
        Set-Default $extKey $ProgId
    }
}

Write-Step ("Claimed " + ($Extensions -join ', '))

# ---------------------------------------------------------------- extras

if ($StartMenu) {
    $programs = [Environment]::GetFolderPath('Programs')
    $lnk = Join-Path $programs 'csv.localhost.lnk'
    $shell = New-Object -ComObject WScript.Shell
    $sc = $shell.CreateShortcut($lnk)
    $sc.TargetPath       = $exe
    $sc.Arguments        = 'home'
    $sc.IconLocation     = "$ico,0"
    $sc.Description      = 'CSV files you have viewed recently'
    $sc.WorkingDirectory = Split-Path -Parent $exe
    $sc.Save()
    Write-Step "Start Menu shortcut created"
}

if ($HostsEntry) {
    $hostsFile = Join-Path $env:SystemRoot 'System32\drivers\etc\hosts'
    try {
        $lines = @(Get-Content $hostsFile -ErrorAction Stop)
        # -match on an array filters it, so test for "no matching lines".
        if (-not ($lines -match '^\s*[^#].*\bcsv\.localhost\b')) {
            Add-Content -Path $hostsFile -Value "`r`n127.0.0.1`tcsv.localhost" -ErrorAction Stop
            Write-Step "Added csv.localhost to the hosts file"
        } else {
            Write-Note "hosts file already mentions csv.localhost"
        }
    } catch {
        Write-Warning "Could not edit the hosts file (run PowerShell as Administrator to use -HostsEntry). Most browsers resolve *.localhost on their own, so this is usually unnecessary."
    }
}

# Tell Explorer that file associations changed, so icons refresh immediately.
if (-not ('CsvLocalhost.Shell32' -as [type])) {
    Add-Type -Namespace CsvLocalhost -Name Shell32 -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("shell32.dll")]
public static extern void SHChangeNotify(int eventId, uint flags, System.IntPtr item1, System.IntPtr item2);
'@
}
[CsvLocalhost.Shell32]::SHChangeNotify(0x08000000, 0x0000, [IntPtr]::Zero, [IntPtr]::Zero)

# ---------------------------------------------------------------- report

Write-Host ""
Write-Host "Done." -ForegroundColor Green
Write-Note "Double-click any .csv file and it opens at http://csv.localhost/"
Write-Note "Commands: csvlocalhost home | status | stop"

if ($needsManual.Count -gt 0) {
    Write-Host ""
    Write-Warning ("Windows already has a default app for: " + ($needsManual -join ', '))
    Write-Host "  Windows does not let a program change that setting for you. To finish:" -ForegroundColor Yellow
    Write-Host "    right-click a .csv file -> Open with -> Choose another app" -ForegroundColor Yellow
    Write-Host "    -> csv.localhost -> tick 'Always use this app'." -ForegroundColor Yellow
}
Write-Host ""
