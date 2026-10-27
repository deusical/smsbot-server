# Build a release from merged main and install it on a Linux host over SSH
# (for example a machine on your Tailscale network running the user systemd unit).
# See docs/GETTING_STARTED.md, "Updating a remote host". Nothing secret is passed on a
# command line: SSH uses your existing authentication, and the vault key stays on the host.
#   .\scripts\deploy-remote.ps1 -Target user@host -OriginHost host.tailnet.ts.net:8443
param(
    [Parameter(Mandatory)][string]$Target,
    # Host header the service expects: host or host:port of its private HTTPS origin,
    # without a scheme (for example host.tailnet.ts.net:8443).
    [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9.-]+(:[0-9]+)?$')][string]$OriginHost,
    # Checkout to build. It must be clean and at origin/main. Defaults to this repository.
    [string]$SourceDir = (Split-Path -Parent $PSScriptRoot)
)
$ErrorActionPreference = 'Stop'
$SourceDir = (Resolve-Path -LiteralPath $SourceDir).Path
$ssh = @('-o', 'BatchMode=yes')
$remoteLib = '.local/lib/smsbot'

function Step([string]$text) { Write-Host "==> $text" }
function Check([string]$what) { if ($LASTEXITCODE -ne 0) { throw "$what failed" } }

Step 'Checking the source is clean merged main'
if (git -C $SourceDir status --porcelain) { throw 'Source checkout has uncommitted changes.' }
git -C $SourceDir fetch --quiet origin main; Check 'git fetch'
$head = (git -C $SourceDir rev-parse HEAD).Trim()
$main = (git -C $SourceDir rev-parse origin/main).Trim()
if ($head -ne $main) { throw "Source is at $head, not origin/main ($main). Merge first, then deploy main." }
$stamp = (git -C $SourceDir describe --tags --always --dirty).Trim()

Step "Building the dashboard and a stripped linux/amd64 binary ($stamp)"
Push-Location (Join-Path $SourceDir 'web')
try {
    if (-not (Test-Path node_modules)) { & npm.cmd ci; Check 'npm ci' }
    & npm.cmd run build; Check 'web build'
} finally { Pop-Location }
Push-Location $SourceDir
$previousEnv = @{GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED}
try {
    & node scripts/sync-web-assets.mjs; Check 'asset sync'
    $env:GOOS = 'linux'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
    New-Item -ItemType Directory -Force -Path bin | Out-Null
    & go build -trimpath "-ldflags=-s -w -X main.version=$stamp" -o bin/smsbot-linux-amd64 ./cmd/smsbot; Check 'go build'
} finally {
    foreach ($name in $previousEnv.Keys) { [Environment]::SetEnvironmentVariable($name, $previousEnv[$name], 'Process') }
    Pop-Location
}
$binary = Join-Path $SourceDir 'bin/smsbot-linux-amd64'
$archive = "$binary.gz"
$source = [IO.File]::OpenRead($binary)
$destination = [IO.File]::Create($archive)
$gzip = New-Object IO.Compression.GZipStream($destination, [IO.Compression.CompressionLevel]::Optimal)
try { $source.CopyTo($gzip) } finally { $gzip.Dispose(); $destination.Dispose(); $source.Dispose() }
$expected = (Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash.ToLowerInvariant()

Step 'Reading the installed binary checksum on the host'
$installed = & ssh @ssh $Target "sha256sum ~/$remoteLib/smsbot"; Check 'ssh'
$previous = (($installed -join ' ') -split '\s+')[0]
if ($previous -notmatch '^[0-9a-f]{64}$') { throw 'Could not read the installed binary checksum.' }
if ($previous -eq $expected) { throw "The host already runs this exact build ($stamp)." }

Step 'Uploading the release and installer'
& scp @ssh $archive "${Target}:$remoteLib/smsbot-$stamp.gz"; Check 'scp release'
& scp @ssh (Join-Path $PSScriptRoot 'remote-install.py') "${Target}:$remoteLib/remote-install.py"; Check 'scp installer'

Step 'Installing (stop, back up state, swap binary, start, health check; rolls back on failure)'
& ssh @ssh $Target "python3 ~/$remoteLib/remote-install.py '$stamp' $expected $previous '$OriginHost'"
if ($LASTEXITCODE -ne 0) { throw 'Remote install failed. The installer restored the previous service if it could; check the host.' }

Step 'Verifying the served dashboard matches this build'
$index = (& ssh @ssh $Target "curl -fsS -H 'Host: $OriginHost' http://127.0.0.1:8787/") -join "`n"; Check 'served index'
$local = Get-Content -Raw -LiteralPath (Join-Path $SourceDir 'web/dist/index.html')
$assets = [regex]::Matches($local, 'assets/[A-Za-z0-9_.-]+\.(?:js|css)') | ForEach-Object Value | Sort-Object -Unique
if (-not $assets) { throw 'No built assets found to compare.' }
foreach ($asset in $assets) {
    if ($index -notmatch [regex]::Escape($asset)) { throw "Host is not serving $asset." }
}
Write-Host "Deployed $stamp to $Target. The service restarts paused: review it, then resume sending yourself."
