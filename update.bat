@echo off
setlocal EnableDelayedExpansion
title Ghost - Updater

set "DIR=%~dp0"
set "SERVER=%DIR%ghost-server.exe"
set "CLIENT=%DIR%ghost.exe"
set "SRVLOG=%DIR%server.log"

echo.
echo  ==========================================
echo   Ghost - Auto Updater
echo  ==========================================
echo.

:: ── Check Go ──────────────────────────────────────────────────────────────
where go >nul 2>&1
if errorlevel 1 (
    echo  [!] Go is not installed. Download from: https://go.dev/dl/
    pause
    exit /b 1
)

:: ── Pull latest code if this is a git repo ────────────────────────────────
where git >nul 2>&1
if not errorlevel 1 (
    if exist "%DIR%.git\" (
        echo  [*] Pulling latest source from git...
        cd /d "%DIR%"
        git pull --ff-only
        if errorlevel 1 (
            echo  [!] Git pull failed. Continuing with local source.
        ) else (
            echo  [+] Source updated.
        )
    ) else (
        echo  [~] No git repo found. Rebuilding from local source.
    )
) else (
    echo  [~] Git not found. Rebuilding from local source.
)

:: ── Stop running server ───────────────────────────────────────────────────
echo.
echo  [*] Stopping relay server if running...
taskkill /f /im ghost-server.exe >nul 2>&1
timeout /t 1 /nobreak >nul

:: ── Rebuild server ────────────────────────────────────────────────────────
echo  [*] Building relay server...
if exist "%SERVER%" del /f "%SERVER%"
cd /d "%DIR%server"
call go mod tidy >nul 2>&1
call go build -ldflags="-s -w" -o "%SERVER%" .
if errorlevel 1 (
    echo  [!] Server build FAILED. Check server\main.go for errors.
    pause
    exit /b 1
)
echo  [+] Server updated.

:: ── Rebuild client ────────────────────────────────────────────────────────
echo  [*] Building Ghost client...
if exist "%CLIENT%" del /f "%CLIENT%"
cd /d "%DIR%client"
call go mod tidy >nul 2>&1
call go build -ldflags="-s -w" -o "%CLIENT%" .
if errorlevel 1 (
    echo  [!] Client build FAILED. Check client\main.go for errors.
    pause
    exit /b 1
)
echo  [+] Client updated.

:: ── Restart server ────────────────────────────────────────────────────────
echo.
echo  [*] Restarting relay server...
cd /d "%DIR%"
start /b "" "%SERVER%" >> "%SRVLOG%" 2>&1
timeout /t 2 /nobreak >nul
netstat -ano | findstr ":8080" | findstr "LISTENING" >nul 2>&1
if errorlevel 1 (
    echo  [!] Server failed to restart. Check server.log.
    pause
    exit /b 1
)
echo  [+] Relay server running on :8080.

:: ── Done ──────────────────────────────────────────────────────────────────
echo.
echo  ==========================================
echo   Update complete! Launch: ghost.bat
echo  ==========================================
echo.
pause
endlocal
