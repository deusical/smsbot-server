param([ValidateSet('Fixture','Google')][string]$Transport = 'Fixture', [switch]$Pair, [switch]$PairNewPhone, [switch]$UpdateCookies)
$ErrorActionPreference = 'Stop'
if ((@($Pair, $PairNewPhone, $UpdateCookies) | Where-Object { $_ }).Count -gt 1) { throw 'Use only one of -Pair, -PairNewPhone or -UpdateCookies.' }
if (($Pair -or $PairNewPhone -or $UpdateCookies) -and $Transport -ne 'Google') { throw 'Pairing and cookie updates require -Transport Google.' }
$binary = Join-Path (Split-Path -Parent $PSScriptRoot) 'bin/smsbot.exe'
if (-not (Test-Path -LiteralPath $binary)) { throw 'Run scriptsuild.ps1 first.' }
$stateRoot = Join-Path $env:LOCALAPPDATA ('SmsBotServer/' + $Transport.ToLowerInvariant())
$state = Join-Path $stateRoot 'state'
$secrets = Join-Path $stateRoot 'vault'
$keyPath = Join-Path $secrets 'master.key'
$ownerPath = Join-Path $secrets 'owner.txt'
if ($UpdateCookies) {
    if (-not (Test-Path -LiteralPath $keyPath)) { throw 'Cookie updates require an existing Google installation.' }
    Write-Host 'Update cookies from your local clipboard offline; preserve the pairing and save an encrypted backup.'
    Get-Clipboard -Raw | & $binary --data $state --key $keyPath --update-cookies --cookies-stdin
    if ($LASTEXITCODE -ne 0) { throw 'Cookie update failed.' }
    exit
}
if ($PairNewPhone) {
    if (-not (Test-Path -LiteralPath $keyPath)) { throw 'Pairing a new phone requires an existing Google installation; use -Pair first.' }
    Write-Host 'Read Google cookies from your local clipboard and approve the emoji on your NEW phone. The old pairing is kept as an encrypted backup. Credentials are never printed.'
    Get-Clipboard -Raw | & $binary --data $state --key $keyPath --pair-new-phone --cookies-stdin
    if ($LASTEXITCODE -ne 0) { throw 'Pairing the new phone failed; the old pairing is unchanged.' }
    exit
}
New-Item -ItemType Directory -Force -Path $secrets | Out-Null
if (-not (Test-Path -LiteralPath $keyPath)) {
    & $binary --data $state --key $keyPath --init --owner-file $ownerPath
    if ($LASTEXITCODE -ne 0) { throw 'Initialization failed.' }
}
if ($Pair) {
    if ($Transport -ne 'Google') { throw 'Use -Transport Google -Pair.' }
    Write-Host 'Read Google cookies from your local clipboard and approve the phone emoji. Credentials are never printed.'
    Get-Clipboard -Raw | & $binary --data $state --key $keyPath --pair --cookies-stdin
    if ($LASTEXITCODE -ne 0) { throw 'Pairing failed.' }
    exit
}
Write-Host "Open http://127.0.0.1:8787. Read your owner credential locally from $ownerPath"
$arguments = @('--data', $state, '--key', $keyPath)
if ($Transport -eq 'Fixture') { $arguments += '--fixture' }
& $binary @arguments
if ($LASTEXITCODE -ne 0) { throw 'Service stopped with an error.' }
