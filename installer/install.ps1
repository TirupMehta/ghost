$ErrorActionPreference = 'Stop'

Write-Host "`n  Ghost CLI Installer for Windows" -ForegroundColor Cyan
Write-Host "  ───────────────────────────────" -ForegroundColor DarkGray

# 1. Determine directories
$installDir = "$HOME\.ghost\bin"
if (!(Test-Path $installDir)) {
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
}

$exePath = "$installDir\ghost.exe"

# 2. Download binary
$downloadUrl = "https://ghost.tirup.in/releases/ghost_windows_amd64.exe"
Write-Host "  → Downloading Ghost binary..." -ForegroundColor Gray
Invoke-WebRequest -Uri $downloadUrl -OutFile $exePath

# 3. Add to User Path if not already present
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    Write-Host "  → Adding $installDir to User PATH..." -ForegroundColor Gray
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
    $env:Path = "$env:Path;$installDir"
}

Write-Host "  ✓ Installed to $exePath`n" -ForegroundColor Green

# 4. Run first-time setup
Write-Host "  → Running handle setup..." -ForegroundColor Gray
try {
    & $exePath --setup
} catch {
    Write-Host "  ! Setup step failed. You can run 'ghost --setup' manually." -ForegroundColor Yellow
}

Write-Host "  ───────────────────────────────" -ForegroundColor DarkGray
Write-Host "  Ghost is installed! Restart your terminal and run 'ghost' to start." -ForegroundColor Green
