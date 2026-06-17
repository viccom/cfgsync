# Stop a running cfgsync instance (foreground or background).
#
# Looks for the process by name; if a PID file exists, also tries that
# directly. Idempotent — exits cleanly if nothing is running.

chcp 65001 > $null
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

Set-Location -Path $PSScriptRoot

$pidPath = Join-Path $PSScriptRoot 'cfgsync.pid'
$pidFromFile = $null
if (Test-Path $pidPath) {
    $pidFromFile = Get-Content $pidPath -ErrorAction SilentlyContinue
}

$procs = @()
if ($pidFromFile) {
    try {
        $p = Get-Process -Id ([int]$pidFromFile) -ErrorAction Stop
        if ($p.ProcessName -eq 'cfgsync-windows-amd64') {
            $procs += $p
        }
    } catch { }
}
if (-not $procs) {
    $procs = @(Get-Process -Name 'cfgsync-windows-amd64' -ErrorAction SilentlyContinue)
}

if (-not $procs) {
    Write-Host 'cfgsync is not running.' -ForegroundColor Yellow
    Remove-Item $pidPath -ErrorAction SilentlyContinue
    exit 0
}

foreach ($p in $procs) {
    try {
        $p | Stop-Process -Force
        Write-Host "Stopped cfgsync (PID $($p.Id))." -ForegroundColor Green
    } catch {
        Write-Host "Failed to stop PID $($p.Id): $_" -ForegroundColor Red
    }
}
Remove-Item $pidPath -ErrorAction SilentlyContinue
