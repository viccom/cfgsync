# cfgsync Windows launcher.
#
# Usage:
#   start-windows.bat              # foreground (default; closes when window closes)
#   start-background.bat           # background (hidden window, logs to cfgsync.log)
#   stop-windows.bat               # stops a running background instance
#
# Or directly from a PowerShell terminal:
#   powershell -NoProfile -ExecutionPolicy Bypass -File start-windows.ps1
#   powershell -NoProfile -ExecutionPolicy Bypass -File start-windows.ps1 -Background
#   powershell -NoProfile -ExecutionPolicy Bypass -File stop-windows.ps1

param(
    [switch]$Background
)

# --- Force UTF-8 for the console and for native-command stdin/stdout ---
# Without this, PowerShell 5.1 on zh-CN Windows defaults to cp936, garbling
# non-ASCII characters in this file and in cfgsync's UTF-8 log output (the
# "鈥?" mojibake).
chcp 65001 > $null
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

$ErrorActionPreference = 'Stop'
Set-Location -Path $PSScriptRoot

$exe = 'cfgsync-windows-amd64.exe'
if (-not (Test-Path $exe)) {
    Write-Host "ERROR: $exe not found in $(Get-Location)" -ForegroundColor Red
    Write-Host 'Build it first: bash scripts\build.sh'
    if (-not $Background) { Read-Host 'Press Enter to close' }
    exit 1
}

# Refuse to start a second instance — cfgsync binds :28972 and a second
# process would fail with "address already in use". Better to surface this
# cleanly than to spawn a doomed process.
$existing = Get-Process -Name 'cfgsync-windows-amd64' -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "ERROR: cfgsync is already running (PID $($existing.Id -join ', '))." -ForegroundColor Red
    Write-Host 'Stop it first with stop-windows.bat.'
    if (-not $Background) { Read-Host 'Press Enter to close' }
    exit 1
}

if ($Background) {
    # Detach: Start-Process spawns an independent process; this PowerShell
    # exits immediately while cfgsync keeps running. Output goes to log
    # files next to the binary so the user can diagnose without a window.
    $logPath  = Join-Path $PSScriptRoot 'cfgsync.log'
    $errPath  = Join-Path $PSScriptRoot 'cfgsync.err.log'
    $pidPath  = Join-Path $PSScriptRoot 'cfgsync.pid'
    # -WindowStyle Hidden keeps the cfgsync console out of the taskbar.
    # -RedirectStandardOutput/Error capture log lines (Go's log.Printf
    # writes to stderr by default, so most lines land in cfgsync.err.log).
    $proc = Start-Process -FilePath ".\$exe" -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $logPath -RedirectStandardError $errPath
    $proc.Id | Out-File -FilePath $pidPath -Encoding ASCII -NoNewline
    Write-Host "cfgsync started in background (PID $($proc.Id))." -ForegroundColor Green
    Write-Host "Log:    $logPath"
    Write-Host "Errors: $errPath"
    Write-Host 'Stop with stop-windows.bat.'
    # Give the user a moment to read the output before the launcher window closes.
    Start-Sleep -Seconds 2
    exit 0
}

# Foreground mode: keep the window open so the user sees live logs.
Write-Host 'Starting cfgsync (foreground)...' -ForegroundColor Green
Write-Host 'Web UI will be at http://127.0.0.1:28972 once the server is up.'
Write-Host 'First run prints a random bootstrap admin password in this window -- copy it.'
Write-Host 'Press Ctrl+C to stop.'
Write-Host ''

$exitCode = 0
try {
    & ".\$exe"
    $exitCode = $LASTEXITCODE
} catch {
    Write-Host ''
    Write-Host "Server crashed: $_" -ForegroundColor Red
    $exitCode = 1
}

Write-Host ''
Write-Host "Server exited with code $exitCode." -ForegroundColor Yellow
Read-Host 'Press Enter to close'
