# herdr-outbox Windows installer
# Usage:
#   irm https://raw.githubusercontent.com/ParadiseWitch/herdr-outbox/main/install.ps1 | iex
#   .\install.ps1 -InstallDir "C:\Tools"

param(
    [string]$InstallDir = "",
    [string]$Version = "main",
    [switch]$Help
)

$ErrorActionPreference = "Stop"

function Write-Info { param($msg) Write-Host "==> " -ForegroundColor Cyan -NoNewline; Write-Host $msg }
function Write-Warn { param($msg) Write-Host "==> " -ForegroundColor Yellow -NoNewline; Write-Host $msg }
function Write-Err { param($msg) Write-Host "==> " -ForegroundColor Red -NoNewline; Write-Host $msg; exit 1 }

if ($Help) {
    Write-Host "Usage: install.ps1 [options]"
    Write-Host ""
    Write-Host "Options:"
    Write-Host "  -InstallDir PATH   Install directory (default: %LOCALAPPDATA%\bin)"
    Write-Host "  -Version TAG       Install specific version (default: main)"
    Write-Host "  -Help              Show this help"
    exit 0
}

$RepoUrl = "https://github.com/ParadiseWitch/herdr-outbox.git"

if (-not $InstallDir) {
    $InstallDir = if ($env:LOCALAPPDATA) { "$env:LOCALAPPDATA\bin" } else { "$HOME\.local\bin" }
}

Write-Info "herdr-outbox Windows installer"
Write-Host ""
Write-Info "Install directory: $InstallDir"
Write-Host ""

# Check prerequisites
$hasGo = $false
$hasGit = $false

try {
    $goVersion = & go version 2>$null
    if ($goVersion) {
        Write-Info "Found Go: $goVersion"
        $hasGo = $true
    }
} catch {}

try {
    $gitVersion = & git --version 2>$null
    if ($gitVersion) {
        Write-Info "Found git: $gitVersion"
        $hasGit = $true
    }
} catch {}

if (-not $hasGit) {
    Write-Err "git is required. Install from: https://git-scm.com/"
}

if (-not $hasGo) {
    Write-Err "Go is required. Install from: https://go.dev/dl/"
}

# Clone and build
$tmpDir = Join-Path $env:TEMP "herdr-outbox-install-$(Get-Random)"
try {
    Write-Info "Cloning repository ($Version)..."
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    
    $cloneArgs = @("clone", "--depth", "1")
    if ($Version -ne "main" -and $Version -ne "latest") {
        $cloneArgs += @("--branch", $Version)
    }
    $cloneArgs += @($RepoUrl, $tmpDir)
    
    & git @cloneArgs
    if ($LASTEXITCODE -ne 0) { throw "git clone failed" }
    
    Write-Info "Building herdr-outbox..."
    Push-Location $tmpDir
    & go build -o "herdr-outbox.exe" ./cmd/herdr-outbox
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Pop-Location
    
    # Install
    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    }
    
    $target = Join-Path $InstallDir "herdr-outbox.exe"
    Copy-Item (Join-Path $tmpDir "herdr-outbox.exe") $target -Force
    
    Write-Info "Installed to $target"
    
    # Add to PATH if needed
    $currentPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ($currentPath -notlike "*$InstallDir*") {
        $newPath = "$InstallDir;$currentPath"
        [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
        Write-Info "Added $InstallDir to user PATH"
        Write-Warn "Restart your terminal or run: `$env:PATH = `"$newPath`""
    }
    
    Write-Host ""
    Write-Info "Installation complete!"
    Write-Info "Run 'herdr-outbox help' for usage"
    Write-Info "Run 'herdr-outbox server' to start the background service"
    
} finally {
    Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
}
