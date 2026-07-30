[CmdletBinding()]
param(
    [ValidateRange(1, 65535)]
    [int]$Port = 5322,
    [string]$Serial = ""
)

$ErrorActionPreference = "Stop"

$adbCommand = Get-Command adb -ErrorAction SilentlyContinue
if ($null -eq $adbCommand) {
    throw "adb was not found. Install Android Platform Tools and add adb to PATH."
}

$deviceArgs = @()
if (-not [string]::IsNullOrWhiteSpace($Serial)) {
    $deviceArgs = @("-s", $Serial)
}

& $adbCommand.Source @deviceArgs get-state | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "No authorized Android device is available."
}

& $adbCommand.Source @deviceArgs forward "tcp:$Port" "tcp:$Port"
if ($LASTEXITCODE -ne 0) {
    throw "ADB port forwarding failed."
}

Write-Host "ZCR521 USB forwarding is ready." -ForegroundColor Green
Write-Host "MCP URL: http://127.0.0.1:$Port/mcp"
Write-Host "For STDIO-only clients: zcr521-bridge.exe --url http://127.0.0.1:$Port/mcp"
