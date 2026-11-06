<#
.SYNOPSIS
Starts a SmsBot video worker on this Windows PC, for a SmsBot running on another
machine. Runs in the foreground; stop it with Ctrl+C.

.DESCRIPTION
The worker listens only on this PC's Tailscale address, which encrypts and
authenticates the link to the SmsBot server. It downloads and compresses videos
with NVENC H.264; it never sends anything. Originals and send copies stay as
plain files under -OutputRoot.

The server needs the same token: copy the token file to the server once and
point its worker configuration at it. The token is never printed.

.EXAMPLE
.\scripts\start-worker.ps1 -Root D:\Project\douyin-downloader
#>
param(
    # Folder holding research\rednote_downloader and research\.oss-venv.
    [Parameter(Mandatory = $true)][string]$Root,
    # Where originals, send copies and job state are kept.
    [string]$OutputRoot = (Join-Path $env:USERPROFILE "Downloads\smsbot"),
    [string]$TokenFile = (Join-Path $env:LOCALAPPDATA "SmsBot\download-worker-token"),
    # This PC's Tailscale 100.x address; found with `tailscale ip -4` when omitted.
    [string]$Bind = "",
    [int]$Port = 8767
)
$ErrorActionPreference = "Stop"

$python = Join-Path $Root "research\.oss-venv\Scripts\python.exe"
if (-not (Test-Path $python)) { throw "Python environment not found: $python" }
if (-not (Test-Path (Join-Path $Root "research\rednote_downloader\app\downloader.py"))) {
    throw "Rednote downloader not found under $Root\research"
}
if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue) -or -not (Get-Command ffprobe -ErrorAction SilentlyContinue)) {
    throw "ffmpeg and ffprobe must be on PATH"
}
if (-not ((& ffmpeg -hide_banner -encoders 2>$null) -match "h264_nvenc")) {
    throw "This ffmpeg has no h264_nvenc encoder (NVIDIA GPU and driver needed)"
}

if (-not $Bind) {
    $tailscale = Get-Command tailscale -ErrorAction SilentlyContinue
    $exe = if ($tailscale) { $tailscale.Source } else { Join-Path $env:ProgramFiles "Tailscale\tailscale.exe" }
    if (-not (Test-Path $exe)) { throw "Tailscale not found; pass -Bind with this PC's 100.x address" }
    $Bind = ((& $exe ip -4) | Select-Object -First 1).Trim()
}

New-Item -ItemType Directory -Force (Split-Path $TokenFile) | Out-Null
New-Item -ItemType Directory -Force $OutputRoot | Out-Null
$worker = Join-Path $PSScriptRoot "download-worker.py"
Write-Host "Worker for SmsBot: http://${Bind}:$Port/download (token file: $TokenFile)"
& $python $worker --root $Root --output-root $OutputRoot --token-file $TokenFile --bind $Bind --port $Port
