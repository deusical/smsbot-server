#requires -Version 7.0
# Run locally after copying /web/config as cURL (bash). Never execute that text.
param([Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9_.@-]+$')][string]$Target)
$ErrorActionPreference = 'Stop'
$request = Get-Clipboard -Raw
if ([string]::IsNullOrWhiteSpace($request) -or [Text.Encoding]::UTF8.GetByteCount($request) -gt 32768) {
    throw 'Copy the /web/config request as cURL (bash) first (maximum 32 KiB).'
}
# Only this fixed program is a command argument. Credentials travel on stdin.
$program = @'
import pathlib, subprocess, sys
payload = sys.stdin.buffer.read(32769)
if not payload or len(payload) > 32768:
    sys.exit("Invalid pairing input")
root = pathlib.Path.home()
binary = root / ".local/lib/smsbot/smsbot"
state = root / ".local/share/smsbot/state"
key = root / ".local/share/smsbot/vault/master.key"
if not binary.is_file() or not key.is_file():
    sys.exit("Existing SmsBot installation required")
subprocess.run(["systemctl", "--user", "stop", "smsbot"], check=True)
try:
    result = subprocess.run([str(binary), "--data", str(state), "--key", str(key),
        "--pair-new-phone", "--cookies-stdin"], input=payload, timeout=200)
finally:
    payload = b""
    subprocess.run(["systemctl", "--user", "start", "smsbot"], check=True)
sys.exit(result.returncode)
'@
$encoded = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($program))
$start = [Diagnostics.ProcessStartInfo]::new()
$start.FileName = 'ssh'
$start.UseShellExecute = $false
$start.RedirectStandardInput = $true
$start.StandardInputEncoding = [Text.UTF8Encoding]::new($false)
foreach ($argument in @('-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', '--', $Target,
    "python3 -c 'import base64;exec(base64.b64decode(`"$encoded`"))'")) {
    $start.ArgumentList.Add($argument)
}
Write-Host 'Pairing on the remote host. Approve the emoji on your NEW phone.'
$process = [Diagnostics.Process]::Start($start)
try {
    $process.StandardInput.Write($request)
    $process.StandardInput.Close()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw 'Pairing did not complete. The helper attempted to restart SmsBot; check its status before retrying.' }
    Write-Host 'Paired. SmsBot restarted paused. Check Settings > Devices, review new conversations, then switch to Live.'
} finally {
    if ((Get-Clipboard -Raw) -eq $request) { Set-Clipboard -Value '' }
    $request = $null
    $process.Dispose()
}
