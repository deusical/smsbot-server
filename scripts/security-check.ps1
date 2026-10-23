$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
$previousToolchain = $env:GOTOOLCHAIN
$previousTemp = $env:GOTMPDIR
try {
    New-Item -ItemType Directory -Force .build-temp | Out-Null
    $env:GOTMPDIR = Join-Path $root '.build-temp'
    $env:GOTOOLCHAIN = 'go1.27.3'
    & go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go vulnerability check failed.' }
    Push-Location web
    try { & npm.cmd audit --omit=dev; if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency audit failed.' } } finally { Pop-Location }
} finally { $env:GOTOOLCHAIN = $previousToolchain; $env:GOTMPDIR = $previousTemp; Pop-Location }
