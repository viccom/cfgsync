@echo off
REM ASCII-only launcher. The real work is in start-windows.ps1; cmd handles
REM Unicode poorly (default codepage is cp936/ANSI on zh-CN Windows, garbling
REM non-ASCII output), so we keep this file bare and let PowerShell render
REM all the user-facing text.

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0start-windows.ps1"
