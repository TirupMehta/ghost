@echo off
setlocal EnableDelayedExpansion
title Ghost - Ephemeral Encrypted Chat

set "DIR=%~dp0"
set "SERVER=%DIR%ghost-server.exe"
set "CLIENT=%DIR%ghost.exe"
set "SRVLOG=%DIR%server.log"

echo.
echo  ==========================================
echo   Ghost - Ephemeral Encrypted Chat
echo  ==========================================
echo.

:: Check Go is installed
where go >nul 2>&1
if errorlevel 1 (
    echo  [!] Go is not installed.
    echo      Download from: https://go.dev/dl/
    echo.
    pause
    exit /b 1
)

:: Build server if missing
if not exist "%SERVER%" (
    echo  [*] Building relay server...
    cd /d "%DIR%server"
    go mod tidy >nul 2>&1
    go build -ldflags="-s -w" -o "%SERVER%" .
    if errorlevel 1 (
        echo  [!] Server build failed.
        pause
        exit /b 1
    )
    echo  [+] Server built.
    cd /d "%DIR%"
)

:: Build client if missing
if not exist "%CLIENT%" (
    echo  [*] Building Ghost client...
    cd /d "%DIR%client"
    go mod tidy >nul 2>&1
    go build -ldflags="-s -w" -o "%CLIENT%" .
    if errorlevel 1 (
        echo  [!] Client build failed.
        pause
        exit /b 1
    )
    echo  [+] Client built.
    cd /d "%DIR%"
)

:: Start server if port 8080 is free
netstat -ano | findstr ":8080" | findstr "LISTENING" >nul 2>&1
if errorlevel 1 (
    echo  [*] Starting relay server on :8080...
    start /b "" "%SERVER%" >> "%SRVLOG%" 2>&1
    timeout /t 2 /nobreak >nul
    netstat -ano | findstr ":8080" | findstr "LISTENING" >nul 2>&1
    if errorlevel 1 (
        echo  [!] Server failed to start. See server.log for details.
        pause
        exit /b 1
    )
    echo  [+] Relay server running.
) else (
    echo  [+] Relay server already running.
)

echo.
cd /d "%DIR%"
"%CLIENT%"
endlocal
