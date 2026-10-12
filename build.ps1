param([switch]$SkipWeb)
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
$previousGoTemp = $env:GOTMPDIR
try {
    New-Item -ItemType Directory -Path '.build-temp' -Force | Out-Null
    $env:GOTMPDIR = Join-Path $PSScriptRoot '.build-temp'
    if (-not $SkipWeb) {
        Push-Location web
        try { & npm.cmd ci; if ($LASTEXITCODE -ne 0) { throw 'Dependency installation failed' }; & npm.cmd run build; if ($LASTEXITCODE -ne 0) { throw 'Web build failed' } } finally { Pop-Location }
        Copy-Item web/dist/* internal/httpapi/assets -Recurse -Force
    }
    & go test -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw 'Offline tests failed' }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
    & go build -trimpath -o bin/smsbot.exe ./cmd/smsbot
    if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
} finally { $env:GOTMPDIR = $previousGoTemp; Pop-Location }
