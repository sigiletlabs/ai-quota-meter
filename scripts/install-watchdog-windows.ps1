# Install the watchdog as a Windows Scheduled Task.
#
# The Windows counterpart of install-watchdog.sh. Registered in the CURRENT
# USER's context for the same reason the others are user-scoped: it reads
# %USERPROFILE%\.claude.json and the ntfy config, both of which are the user's.
#
# UNTESTED ON REAL HARDWARE. Written against the ScheduledTasks module
# documentation and not run on Windows. If it misbehaves, that is worth an
# issue rather than a workaround.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\install-watchdog-windows.ps1
#   powershell -ExecutionPolicy Bypass -File .\scripts\install-watchdog-windows.ps1 -Uninstall

param([switch]$Uninstall)

$ErrorActionPreference = 'Stop'
$TaskName = 'ai-quota-meter-watchdog'
$Binary   = Join-Path $env:LOCALAPPDATA 'Programs\ai-quota-meter\ai-quota-meter.exe'

if ($Uninstall) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "removed scheduled task $TaskName"
    exit 0
}

if (-not (Test-Path $Binary)) {
    Write-Error "binary not found at $Binary. Put ai-quota-meter.exe there, or edit `$Binary in this script."
}

# Six-hourly, matching the systemd timer and the launchd agent. The threshold
# it checks is 48 hours, so this is far more often than it needs to be; the
# point is that a machine which was asleep reports soon after waking.
$triggers = @(0, 6, 12, 18) | ForEach-Object {
    New-ScheduledTaskTrigger -Daily -At ([datetime]::Today.AddHours($_).AddMinutes(7))
}

$action = New-ScheduledTaskAction -Execute $Binary -Argument '--watchdog'

# StartWhenAvailable is systemd's Persistent=true: run a missed occurrence
# rather than skipping it. A watchdog that silently does not run is the exact
# failure it exists to catch.
$settings = New-ScheduledTaskSettingsSet `
    -StartWhenAvailable `
    -RandomDelay (New-TimeSpan -Minutes 5) `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 5) `
    -DontStopIfGoingOnBatteries `
    -AllowStartIfOnBatteries

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $triggers `
    -Settings $settings -Description 'Check that the Claude quota watch is still receiving readings' `
    -Force | Out-Null

Write-Host "registered scheduled task $TaskName"
Get-ScheduledTask -TaskName $TaskName | Format-List TaskName, State
Write-Host ""
Write-Host "Run it once now to prove the path works:"
Write-Host "  $Binary --watchdog"
