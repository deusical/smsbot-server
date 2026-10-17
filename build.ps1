param(
    [switch]$SkipWeb,
    [switch]$SkipInstall,
    [switch]$SkipChecks,
    [switch]$DebugSymbols
)
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
$previousGoTemp = $env:GOTMPDIR
try {
    New-Item -ItemType Directory -Path '.build-temp' -Force | Out-Null
    $env:GOTMPDIR = Join-Path $PSScriptRoot '.build-temp'
    if (-not $SkipWeb) {
        Push-Location web
        try {
            if (-not $SkipInstall) {
                & npm.cmd ci
                if ($LASTEXITCODE -ne 0) { throw 'Dependency installation failed' }
            }
            & npm.cmd run build
            if ($LASTEXITCODE -ne 0) { throw 'Web build failed' }
        } finally { Pop-Location }
        # Vite uses content-hashed filenames. Replace the embedded file set so
        # previous dashboard versions cannot accumulate inside the executable.
        & node scripts/sync-web-assets.mjs
        if ($LASTEXITCODE -ne 0) { throw 'Embedded asset synchronization failed' }
    }
    if (-not $SkipChecks) {
        # Reuse Go's content-keyed test cache for unchanged offline packages.
        # CI still uses -race -count=1 to force a fresh validation run.
        & go test ./...
        if ($LASTEXITCODE -ne 0) { throw 'Offline tests failed' }
        & go vet ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
    }
    $buildArguments = @('build', '-trimpath')
    $stamp = (& git describe --tags --always --dirty 2>$null)
    if (-not $stamp) { $stamp = 'dev' }
    if ($DebugSymbols) { $buildArguments += "-ldflags=-X main.version=$stamp" }
    else { $buildArguments += "-ldflags=-s -w -X main.version=$stamp" }
    $buildArguments += @('-o', 'bin/smsbot.exe', './cmd/smsbot')
    & go @buildArguments
    if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
} finally { $env:GOTMPDIR = $previousGoTemp; Pop-Location }
