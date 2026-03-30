# PowerShell install script for cloudcent (Windows)
# Usage: irm https://raw.githubusercontent.com/CloudCentIO/cost-estimator-cli-rs/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

$Repo = "CloudCentIO/cost-estimator-cli-rs"
$Binary = "cloudcent"
$InstallDir = "$env:USERPROFILE\.cloudcent\bin"

function Detect-Arch {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    switch ($arch) {
        "X64"   { return "x86_64" }
        "Arm64" { return "aarch64" }
        default {
            Write-Error "Unsupported architecture: $arch"
            exit 1
        }
    }
}

function Get-LatestVersion {
    $url = "https://api.github.com/repos/$Repo/releases/latest"
    $response = Invoke-RestMethod -Uri $url -Headers @{ "User-Agent" = "cloudcent-installer" }
    return $response.tag_name
}

function Add-ToPath($dir) {
    $currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($currentPath -notlike "*$dir*") {
        [Environment]::SetEnvironmentVariable("Path", "$currentPath;$dir", "User")
        $env:Path = "$env:Path;$dir"
        Write-Host "Added $dir to your PATH (restart your terminal for it to take effect)."
    }
}

function Main {
    $arch = Detect-Arch
    $platform = "windows-$arch"
    Write-Host "Detected platform: $platform"

    Write-Host "Fetching latest release..."
    $version = Get-LatestVersion
    if (-not $version) {
        Write-Error "Could not determine latest version. Check https://github.com/$Repo/releases"
        exit 1
    }
    Write-Host "Latest version: $version"

    $archiveName = "$Binary-$platform.zip"
    $url = "https://github.com/$Repo/releases/download/$version/$archiveName"

    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString())
    New-Item -ItemType Directory -Path $tmpDir | Out-Null

    try {
        Write-Host "Downloading $url..."
        Invoke-WebRequest -Uri $url -OutFile (Join-Path $tmpDir $archiveName) -UseBasicParsing

        Write-Host "Extracting..."
        Expand-Archive -Path (Join-Path $tmpDir $archiveName) -DestinationPath $tmpDir -Force

        if (-not (Test-Path $InstallDir)) {
            New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        }

        $src = Join-Path $tmpDir "$Binary.exe"
        $dest = Join-Path $InstallDir "$Binary.exe"
        Move-Item -Path $src -Destination $dest -Force

        Add-ToPath $InstallDir

        Write-Host ""
        Write-Host "Done! Run 'cloudcent' to get started."
    }
    finally {
        Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
    }
}

Main
