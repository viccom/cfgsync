@echo off
REM ASCII-only shim. Launches cfgsync in background mode (hidden window,
REM logs to cfgsync.log). See start-windows.ps1 for the real logic.

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0start-windows.ps1" -Background
