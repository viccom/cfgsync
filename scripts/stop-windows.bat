@echo off
REM ASCII-only shim. Stops a running cfgsync instance.

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0stop-windows.ps1"
