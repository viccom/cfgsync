# cfgsync Windows launcher.
#
# Run via start-windows.bat (which calls this with -ExecutionPolicy Bypass),
# or directly from a PowerShell terminal:
#     powershell -NoProfile -ExecutionPolicy Bypass -File start-windows.ps1
#
# On first run, cfgsync itself generates cfgsync.env next to the binary
# with a random JWT_SECRET and a random bootstrap admin password; the
# server log prints the password once. Edit cfgsync.env and restart to
# apply changes.

$ErrorActionPreference = 'Stop'
Set-Location -Path $PSScriptRoot

$exe = 'cfgsync-windows-amd64.exe'
if (-not (Test-Path $exe)) {
    Write-Host "ERROR: $exe not found in $(Get-Location)" -ForegroundColor Red
    Write-Host 'Build it first: bash scripts\build.sh'
    Read-Host 'Press Enter to close'
    exit 1
}

Write-Host 'Starting cfgsync...' -ForegroundColor Green
Write-Host 'Web UI will be at http://127.0.0.1:28972 once the server is up.'
Write-Host 'First run prints a random bootstrap admin password in this window — copy it.'
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
