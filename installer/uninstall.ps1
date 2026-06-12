$ErrorActionPreference = 'Stop'

Write-Host "`n  Ghost CLI Uninstaller for Windows" -ForegroundColor Cyan
Write-Host "  ─────────────────────────────────" -ForegroundColor DarkGray

$installDir = "$HOME\.ghost"
$binDir = "$installDir\bin"

# 1. Remove files and configuration folder
if (Test-Path $installDir) {
    Write-Host "  → Removing $installDir directory..." -ForegroundColor Gray
    try {
        Remove-Item -Recurse -Force -Path $installDir
        Write-Host "  ✓ Removed configuration and binaries." -ForegroundColor Green
    } catch {
        Write-Host "  ! Failed to delete some files in $installDir. Close any running instances of Ghost and try again." -ForegroundColor Yellow
    }
} else {
    Write-Host "  → No Ghost directory found at $installDir." -ForegroundColor Gray
}

# 2. Clean up PATH env variable
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -like "*$binDir*") {
    Write-Host "  → Removing $binDir from User PATH..." -ForegroundColor Gray
    
    # Split, filter, and re-join path entries
    $paths = $userPath -split ";" | Where-Object { $_ -ne $binDir -and $_ -ne "" }
    $newPath = $paths -join ";"
    
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    
    # Update current session path
    $envPathParts = $env:Path -split ";" | Where-Object { $_ -ne $binDir -and $_ -ne "" }
    $env:Path = $envPathParts -join ";"
    
    Write-Host "  ✓ Removed from PATH." -ForegroundColor Green
}

Write-Host "  ─────────────────────────────────" -ForegroundColor DarkGray
Write-Host "  Ghost has been successfully uninstalled!" -ForegroundColor Green
Write-Host "`n  (Note: You may need to restart any open terminals for PATH changes to take full effect.)`n" -ForegroundColor Gray
