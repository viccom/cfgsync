@echo off
REM Double-click launcher for cfgsync on Windows.
REM
REM On first run, cfgsync itself generates cfgsync.env next to the binary
REM with a random JWT_SECRET and a random bootstrap admin password; the
REM server log prints the password once. Edit cfgsync.env to taste and
REM restart to apply changes.

cd /d "%~dp0"

set "EXE=cfgsync-windows-amd64.exe"
if not exist "%EXE%" (
    echo ERROR: %EXE% not found in %CD%.
    echo Build it first: bash scripts\build.sh
    pause
    exit /b 1
)

echo Starting cfgsync...
echo Web UI will be at http://127.0.0.1:28972 once the server is up.
echo First run prints a random bootstrap admin password in this window — copy it.
echo Press Ctrl+C to stop.
echo.

"%EXE%"

echo.
echo Server exited with code %ERRORLEVEL%. Press any key to close.
pause >nul
