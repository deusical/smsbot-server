<#
.SYNOPSIS
Starts a SmsBot video worker on this Windows PC, for a SmsBot running on another
machine. Runs in the foreground by default; stop it with Ctrl+C.

.DESCRIPTION
The worker listens only on 127.0.0.1 or this PC's Tailscale address, which
encrypts and authenticates the link to the SmsBot server. It downloads and
compresses videos with NVENC H.264; it never sends anything. Originals and send
copies stay as plain files under -OutputRoot.

The server needs the same token: copy the token file to the server once and
point its worker configuration at it. The token is never printed.

Pass -Install to register and start a per-user Scheduled Task ("SmsBot worker")
that launches at logon without admin rights and waits for Tailscale to come up.
Pass -Uninstall to stop and remove that task and its worker process.

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
    # 127.0.0.1 or this PC's Tailscale 100.x address; found with `tailscale ip -4` when omitted.
    [string]$Bind = "",
    [ValidateRange(1, 65535)][int]$Port = 8767,
    # Jobs run at once.
    [ValidateRange(1, 8)][int]$Slots = 1,
    # Delete terminal job folders older than this many days (0 = keep everything).
    [ValidateRange(0, 36500)][int]$KeepDays = 0,
    # Wait indefinitely for the Tailscale/bind address and relaunch if it disappears.
    [switch]$WaitForTailscale,
    # Maximum seconds to wait for the bind address when -WaitForTailscale is not set.
    [ValidateRange(0, 86400)][int]$WaitTimeoutSeconds = 30,
    # Scheduled task name used by -Install and -Uninstall.
    [ValidateNotNullOrEmpty()][string]$TaskName = "SmsBot worker",
    # Register and start the per-user "SmsBot worker" logon scheduled task.
    [switch]$Install,
    # Stop and remove the "SmsBot worker" scheduled task.
    [switch]$Uninstall
)
$ErrorActionPreference = "Stop"

function Resolve-FullPath([string]$PathValue) {
    return [System.IO.Path]::GetFullPath($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($PathValue))
}

function Quote-TaskArg([string]$Value) {
    if ($Value -match '[\s"]') {
        return '"' + ($Value -replace '(\\*)"', '$1$1\"' -replace '(\\+)$', '$1$1') + '"'
    }
    return $Value
}

function Get-TaskOutputRoots($Task) {
    # -Install writes -OutputRoot either bare or double-quoted (Quote-TaskArg);
    # Windows paths cannot contain '"', so a simple match reads it back.
    $roots = @()
    if (-not $Task) {
        return $roots
    }
    foreach ($act in @($Task.Actions)) {
        if ($act -and $act.Arguments -match '-OutputRoot\s+(?:"([^"]+)"|(\S+))') {
            $value = if ($Matches[1]) { $Matches[1] } else { $Matches[2] }
            $roots += (Resolve-FullPath $value)
        }
    }
    return $roots
}

function Stop-ScheduledTaskAndWorker($Task, [string]$Name, [string[]]$ExtraOutputRoots = @()) {
    if ($Task -and $Task.State -eq "Running") {
        Stop-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
        $stopDeadline = [DateTime]::UtcNow.AddSeconds(3)
        while ([DateTime]::UtcNow -lt $stopDeadline) {
            $current = Get-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
            if (-not $current -or $current.State -ne "Running") {
                break
            }
            Start-Sleep -Milliseconds 100
        }
    }

    $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([System.StringComparer]::OrdinalIgnoreCase)
    $candidateRoots = New-Object System.Collections.Generic.List[string]
    # @() on both sides: a single root comes back as a string, and string + array
    # would concatenate text instead of joining the lists.
    foreach ($pathItem in (@(Get-TaskOutputRoots $Task) + @($ExtraOutputRoots))) {
        if (-not [string]::IsNullOrWhiteSpace($pathItem)) {
            $resolved = Resolve-FullPath $pathItem
            if ($seen.Add($resolved)) {
                $candidateRoots.Add($resolved)
            }
        }
    }

    foreach ($dir in $candidateRoots) {
        $pidFile = Join-Path $dir "worker.pid"
        if (-not (Test-Path -LiteralPath $pidFile)) {
            continue
        }
        $rawPid = ""
        try {
            $rawPid = (Get-Content -LiteralPath $pidFile -Raw -ErrorAction Stop).Trim()
        } catch {
            $rawPid = ""
        }
        $workerPid = 0
        if ([int]::TryParse($rawPid, [ref]$workerPid) -and $workerPid -gt 0) {
            $proc = $null
            try {
                $proc = Get-CimInstance Win32_Process -Filter "ProcessId = $workerPid" -ErrorAction Stop
            } catch {
                $proc = $null
            }
            if ($null -ne $proc -and
                $proc.Name -match '^pythonw?(\.exe)?$' -and
                $null -ne $proc.CommandLine -and
                $proc.CommandLine -match 'download-worker\.py' -and
                $proc.CommandLine.IndexOf($dir, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
                $pidsToStop = New-Object System.Collections.Generic.List[int]
                $pidsToStop.Add([int]$workerPid)
                if ($proc.ParentProcessId -gt 0) {
                    $parentProc = $null
                    try {
                        $parentProc = Get-CimInstance Win32_Process -Filter "ProcessId = $($proc.ParentProcessId)" -ErrorAction Stop
                    } catch {
                        $parentProc = $null
                    }
                    if ($null -ne $parentProc -and
                        $parentProc.Name -match '^pythonw?(\.exe)?$' -and
                        $null -ne $parentProc.CommandLine -and
                        $parentProc.CommandLine -match 'download-worker\.py' -and
                        $parentProc.CommandLine.IndexOf($dir, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
                        $pidsToStop.Add([int]$parentProc.ProcessId)
                    }
                }
                foreach ($targetPid in $pidsToStop) {
                    $prevEap = $ErrorActionPreference
                    try {
                        $ErrorActionPreference = "Continue"
                        & taskkill.exe /PID $targetPid /T /F >$null 2>$null
                    } catch {} finally {
                        $ErrorActionPreference = $prevEap
                    }
                    try {
                        Stop-Process -Id $targetPid -Force -Confirm:$false -ErrorAction Stop
                    } catch {}
                }
                $exitDeadline = [DateTime]::UtcNow.AddSeconds(3)
                while ([DateTime]::UtcNow -lt $exitDeadline) {
                    $anyAlive = $false
                    foreach ($targetPid in $pidsToStop) {
                        if (Get-Process -Id $targetPid -ErrorAction SilentlyContinue) {
                            $anyAlive = $true
                            break
                        }
                    }
                    if (-not $anyAlive) {
                        break
                    }
                    Start-Sleep -Milliseconds 100
                }
            }
        }
        try {
            Remove-Item -LiteralPath $pidFile -Force -Confirm:$false -ErrorAction Stop
        } catch {}
    }
}

function Test-TailnetIPv4([string]$Address) {
    if ($Address -notmatch '^(?:0|[1-9][0-9]{0,2})(?:\.(?:0|[1-9][0-9]{0,2})){3}$') {
        return $false
    }
    $ip = $null
    if (-not [System.Net.IPAddress]::TryParse($Address, [ref]$ip) -or $ip.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) {
        return $false
    }
    $bytes = $ip.GetAddressBytes()
    return ($bytes[0] -eq 100 -and ($bytes[1] -band 0xC0) -eq 64)
}

function Assert-ValidBindAddress([string]$Address) {
    if ($Address -eq "127.0.0.1" -or (Test-TailnetIPv4 $Address)) {
        return $Address
    }
    throw "Bind address '$Address' must be 127.0.0.1 or a Tailscale IPv4 address in 100.64.0.0/10."
}

function Test-LocalAddressReady([string]$Address) {
    if ($Address -eq "127.0.0.1") {
        return $true
    }
    $interfaces = @()
    try {
        $interfaces = [System.Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces()
    } catch {
        return $false
    }
    foreach ($iface in $interfaces) {
        if ($iface.OperationalStatus -ne [System.Net.NetworkInformation.OperationalStatus]::Up) {
            continue
        }
        try {
            $props = $iface.GetIPProperties()
            foreach ($uni in $props.UnicastAddresses) {
                if ($null -ne $uni.Address -and $uni.Address.IPAddressToString -eq $Address) {
                    return $true
                }
            }
        } catch {
            continue
        }
    }
    return $false
}

function Get-TailscaleIPv4([string]$TailscaleExe) {
    if ([string]::IsNullOrWhiteSpace($TailscaleExe) -or -not (Test-Path -LiteralPath $TailscaleExe)) {
        return $null
    }
    $prevEap = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $output = & $TailscaleExe ip -4 2>$null
        if ($LASTEXITCODE -ne 0 -or -not $output) {
            return $null
        }
        foreach ($line in @($output)) {
            $candidate = ([string]$line).Trim()
            if ($candidate -and (Test-TailnetIPv4 $candidate)) {
                return $candidate
            }
        }
        return $null
    } catch {
        return $null
    } finally {
        $ErrorActionPreference = $prevEap
    }
}

function Resolve-ReadyBindAddress([string]$ConfiguredBind, [string]$TailscaleExe, [bool]$WaitIndefinitely, [int]$TimeoutSeconds) {
    $delay = 2
    $statusIntervalSeconds = 300
    $waitedOnce = $false
    $lastStatusAt = [DateTime]::MinValue
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastTailscaleCandidate = $null

    while ($true) {
        $candidate = $null
        if ($ConfiguredBind) {
            $candidate = $ConfiguredBind
        } else {
            $candidate = Get-TailscaleIPv4 $TailscaleExe
            if ($candidate) {
                $lastTailscaleCandidate = $candidate
            }
        }

        if ($candidate -and (Test-LocalAddressReady $candidate)) {
            return $candidate
        }

        $now = [DateTime]::UtcNow
        $remaining = ($deadline - $now).TotalSeconds
        if (-not $WaitIndefinitely -and $remaining -le 0) {
            if ($ConfiguredBind) {
                throw "Bind address $ConfiguredBind is not assigned to an active local interface after waiting ${TimeoutSeconds}s."
            }
            if ($lastTailscaleCandidate) {
                throw "Tailscale address $lastTailscaleCandidate is not assigned to an active local interface after waiting ${TimeoutSeconds}s."
            }
            throw "Tailscale returned no IPv4 address after waiting ${TimeoutSeconds}s; ensure Tailscale is connected or pass -Bind."
        }

        if (-not $waitedOnce) {
            if ($ConfiguredBind) {
                Write-Host "Waiting for bind address $ConfiguredBind to be ready on a local interface..."
            } else {
                Write-Host "Waiting for Tailscale IPv4 address to be ready..."
            }
            $waitedOnce = $true
            $lastStatusAt = $now
        } elseif ((($now - $lastStatusAt).TotalSeconds) -ge $statusIntervalSeconds) {
            if ($ConfiguredBind) {
                Write-Host "Still waiting for bind address $ConfiguredBind to be ready on a local interface..."
            } else {
                Write-Host "Still waiting for Tailscale IPv4 address to be ready..."
            }
            $lastStatusAt = $now
        }

        $sleepSeconds = [double]$delay
        if (-not $WaitIndefinitely -and $sleepSeconds -gt $remaining) {
            $sleepSeconds = $remaining
        }
        $sleepMs = [int][Math]::Max(1, [Math]::Ceiling($sleepSeconds * 1000))
        Start-Sleep -Milliseconds $sleepMs
        $delay = [Math]::Min($delay * 2, 30)
    }
}

if ($Install -and $Uninstall) {
    throw "Use only one of -Install or -Uninstall."
}

if ($Uninstall) {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($task) {
        $extraRoots = @()
        if ($PSBoundParameters.ContainsKey("OutputRoot")) {
            $extraRoots += $OutputRoot
        }
        Stop-ScheduledTaskAndWorker -Task $task -Name $TaskName -ExtraOutputRoots $extraRoots
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Write-Host "Removed scheduled task '$TaskName'."
    } else {
        Write-Host "Scheduled task '$TaskName' is not installed."
    }
    exit 0
}

$Bind = $Bind.Trim()
if ($Bind) {
    $Bind = Assert-ValidBindAddress $Bind
}

if ([string]::IsNullOrWhiteSpace($Root)) {
    throw "-Root is required."
}

$Root = (Resolve-Path -LiteralPath $Root).Path
$OutputRoot = Resolve-FullPath $OutputRoot
$TokenFile = Resolve-FullPath $TokenFile

$python = Join-Path $Root "research\.oss-venv\Scripts\python.exe"
if (-not (Test-Path $python)) { throw "Python environment not found: $python" }
if (-not (Test-Path (Join-Path $Root "research\rednote_downloader\app\downloader.py"))) {
    throw "Rednote downloader not found under $Root\research"
}
if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue) -or -not (Get-Command ffprobe -ErrorAction SilentlyContinue)) {
    throw "ffmpeg and ffprobe must be on PATH"
}
$prevEap = $ErrorActionPreference
try {
    $ErrorActionPreference = "Continue"
    $encodersOut = & ffmpeg -hide_banner -encoders 2>$null
} finally {
    $ErrorActionPreference = $prevEap
}
if (-not ($encodersOut -match "h264_nvenc")) {
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
        "-Slots", $Slots,
        "-WaitForTailscale"
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
        -MultipleInstances IgnoreNew `
        -Priority 6
    # No -RestartCount: Task Scheduler restarts only when it cannot launch the
    # action, never when the script exits non-zero. The script's own loop
    # waits for Tailscale and relaunches a worker that crashes after running.

    $existing = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($existing) {
        Stop-ScheduledTaskAndWorker -Task $existing -Name $TaskName -ExtraOutputRoots @($OutputRoot)
    }
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
    Start-ScheduledTask -TaskName $TaskName
    Write-Host "Installed and started scheduled task '$TaskName' for $currentUser (token file: $TokenFile)."
    exit 0
}

$tailscaleExe = $null
if (-not $Bind) {
    $tailscaleExe = Find-TailscaleExe
}

New-Item -ItemType Directory -Force (Split-Path $TokenFile) | Out-Null
New-Item -ItemType Directory -Force $OutputRoot | Out-Null
$worker = Join-Path $PSScriptRoot "download-worker.py"
$addrNotAvailableExitCode = 75
$relaunchDelay = 2
$lastRelaunchStatusAt = [DateTime]::MinValue

while ($true) {
    $activeBind = Resolve-ReadyBindAddress -ConfiguredBind $Bind -TailscaleExe $tailscaleExe -WaitIndefinitely ([bool]$WaitForTailscale) -TimeoutSeconds $WaitTimeoutSeconds
    Write-Host "Worker for SmsBot: http://${activeBind}:$Port/download (token file: $TokenFile)"
    $workerArgs = @(
        $worker,
        "--root", $Root,
        "--output-root", $OutputRoot,
        "--token-file", $TokenFile,
        "--bind", $activeBind,
        "--port", $Port,
        "--slots", $Slots
    )
    if ($KeepDays -gt 0) {
        $workerArgs += @("--keep-days", $KeepDays)
    }
    $startedAt = [DateTime]::UtcNow
    & $python @workerArgs
    $exitCode = $LASTEXITCODE
    if ($exitCode -eq 0) {
        exit 0
    }
    # As a logon task, keep the worker up: retry an address that is not ready
    # yet, and relaunch a worker that exits after running at least a minute.
    # A worker that fails quickly for another reason (bad token file, port
    # already taken) is a setup problem, so the script stops instead.
    $ranLong = (([DateTime]::UtcNow) - $startedAt).TotalSeconds -ge 60
    if ($WaitForTailscale -and ($exitCode -eq $addrNotAvailableExitCode -or $ranLong)) {
        if ($ranLong) {
            $relaunchDelay = 2
        }
        $now = [DateTime]::UtcNow
        if ($lastRelaunchStatusAt -eq [DateTime]::MinValue -or ((($now - $lastRelaunchStatusAt).TotalSeconds) -ge 300)) {
            if ($exitCode -eq $addrNotAvailableExitCode) {
                Write-Host "Bind address $activeBind is unavailable; waiting before retrying..."
            } else {
                Write-Host "Worker exited with code $exitCode; restarting..."
            }
            $lastRelaunchStatusAt = $now
        }
        Start-Sleep -Seconds $relaunchDelay
        $relaunchDelay = [Math]::Min($relaunchDelay * 2, 30)
        continue
    }
    exit $exitCode
}
