param(
    [switch]$NoEnv
)

$ErrorActionPreference = "Stop"

if (-not $NoEnv) {
    $env:PATH = "C:\msys64\mingw64\bin;C:\msys64\usr\bin;" + $env:PATH
    $env:PKG_CONFIG_PATH = "C:\msys64\mingw64\lib\pkgconfig;C:\msys64\usr\lib\pkgconfig"
}

Write-Host "Building picoclaw-livekit..."

go build ./cmd/picoclaw-livekit
