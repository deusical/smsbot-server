<#
.SYNOPSIS
Starts a SmsBot video worker on this Windows PC, for a SmsBot running on another
machine. Runs in the foreground by default; stop it with Ctrl+C.

.DESCRIPTION
The worker listens only on this PC's Tailscale address, which encrypts and
authenticates the link to the SmsBot server. It downloads and compresses videos
with NVENC H.264; it never sends anything. Originals and send copies stay as
plain files under -OutputRoot.

The server needs the same token: copy the token file to the server once and
point its worker configuration at it. The token is never printed.

Pass -Install to register and start a per-user Scheduled Task ("SmsBot worker")
that launches at logon without admin rights. Pass -Uninstall to stop and remove
that task.

.EXAMPLE
.\scripts\start-worker.ps1 -Root C:\Project\douyin-downloader
.EXAMPLE
.\scripts\start-worker.ps1 -Root C:\Project\douyin-downloader -KeepDays 14 -Install
.EXAMPLE
.\scripts\start-worker.ps1 -Uninstall
#>
param(
    # Folder holding research\rednote_downloader and research\.oss-venv.
    [string]$Root = "",
    # Where originals, send copies and job state are kept.
    [string]$OutputRoot = (Join-Path $env:USERPROFILE "Downloads\smsbot"),
    [string]$TokenFile = (Join-Path $env:LOCALAPPDATA "SmsBot\download-worker-token"),
    # This PC's Tailscale 100.x address; found with `tailscale ip -4` when omitted.
    [string]$Bind = "",
    [int]$Port = 8767,
    # Jobs run at once.
    [ValidateRange(1, 8)][int]$Slots = 1,
    # Delete terminal job folders older than this many days (0 = keep everything).
    [ValidateRange(0, 36500)][int]$KeepDays = 0,
    # Register and start the per-user "SmsBot worker" logon scheduled task.
    [switch]$Install,
    # Stop and remove the "SmsBot worker" scheduled task.
    [switch]$Uninstall
)
$ErrorActionPreference = "Stop"
$taskName = "SmsBot worker"

if ($Install -and $Uninstall) {
    throw "Use only one of -Install or -Uninstall."
}

if ($Uninstall) {
    $task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    if ($task) {
        if ($task.State -eq "Running") {
            Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        }
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
        Write-Host "Removed scheduled task '$taskName'."
    } else {
        Write-Host "Scheduled task '$taskName' is not installed."
    }
    exit 0
}

if ([string]::IsNullOrWhiteSpace($Root)) {
    throw "-Root is required."
}

function Resolve-FullPath([string]$PathValue) {
    return [System.IO.Path]::GetFullPath($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($PathValue))
}

function Quote-TaskArg([string]$Value) {
    if ($Value -match '[\s"]') {
        return '"' + ($Value -replace '(\\*)"', '$1$1\"' -replace '(\\+)$', '$1$1') + '"'
    }
    return $Value
}

$Root = (Resolve-Path -LiteralPath $Root).Path
$OutputRoot = Resolve-FullPath $OutputRoot
$TokenFile = Resolve-FullPath $TokenFile
$Bind = $Bind.Trim()

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

function Find-TailscaleExe {
    $tailscale = Get-Command tailscale -ErrorAction SilentlyContinue
    $exe = if ($tailscale) { $tailscale.Source } else { Join-Path $env:ProgramFiles "Tailscale\tailscale.exe" }
    if (-not (Test-Path $exe)) { throw "Tailscale not found; pass -Bind with this PC's 100.x address" }
    return $exe
}

if ($Install) {
    if (-not $Bind) {
        Find-TailscaleExe | Out-Null
    }
    $scriptPath = $PSCommandPath
    $taskArgs = @(
        "-NoProfile",
        "-WindowStyle", "Hidden",
        "-ExecutionPolicy", "Bypass",
        "-File", (Quote-TaskArg $scriptPath),
        "-Root", (Quote-TaskArg $Root),
        "-OutputRoot", (Quote-TaskArg $OutputRoot),
        "-TokenFile", (Quote-TaskArg $TokenFile),
        "-Port", $Port,
        "-Slots", $Slots
    )
    if ($Bind) {
        $taskArgs += @("-Bind", (Quote-TaskArg $Bind))
    }
    if ($KeepDays -gt 0) {
        $taskArgs += @("-KeepDays", $KeepDays)
    }

    $currentUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $action = New-ScheduledTaskAction -Execute "powershell.exe" -Argument ($taskArgs -join " ")
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $currentUser
    $principal = New-ScheduledTaskPrincipal -UserId $currentUser -LogonType Interactive -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -StartWhenAvailable `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1)

    $existing = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    if ($existing -and $existing.State -eq "Running") {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    }
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
    Start-ScheduledTask -TaskName $taskName
    Write-Host "Installed and started scheduled task '$taskName' for $currentUser (token file: $TokenFile)."
    exit 0
}

if (-not $Bind) {
    $exe = Find-TailscaleExe
    $ip = (& $exe ip -4) | Select-Object -First 1
    if ([string]::IsNullOrWhiteSpace($ip)) {
        throw "Tailscale returned no IPv4 address; pass -Bind or ensure Tailscale is connected"
    }
    $Bind = $ip.Trim()
}

New-Item -ItemType Directory -Force (Split-Path $TokenFile) | Out-Null
New-Item -ItemType Directory -Force $OutputRoot | Out-Null
$worker = Join-Path $PSScriptRoot "download-worker.py"
Write-Host "Worker for SmsBot: http://${Bind}:$Port/download (token file: $TokenFile)"
$workerArgs = @(
    $worker,
    "--root", $Root,
    "--output-root", $OutputRoot,
    "--token-file", $TokenFile,
    "--bind", $Bind,
    "--port", $Port,
    "--slots", $Slots
)
if ($KeepDays -gt 0) {
    $workerArgs += @("--keep-days", $KeepDays)
}
& $python @workerArgs
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
