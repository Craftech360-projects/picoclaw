param(
    [switch]$NoEnv
)

$ErrorActionPreference = "Stop"

if (-not $NoEnv) {
    $env:PATH = "C:\msys64\mingw64\bin;C:\msys64\usr\bin;" + $env:PATH
    $env:PKG_CONFIG_PATH = "C:\msys64\mingw64\lib\pkgconfig;C:\msys64\usr\lib\pkgconfig"
}

Write-Host "Building picoclaw-livekit..."

$output = Join-Path $PSScriptRoot "..\\picoclaw-livekit.exe"
go build -o $output ./cmd/picoclaw-livekit
if ($LASTEXITCODE -ne 0) {
    throw "go build failed with exit code $LASTEXITCODE"
}
Write-Host "Built $output"
