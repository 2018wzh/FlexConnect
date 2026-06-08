param(
  [string]$ServiceName = "FlexConnect"
)

$ErrorActionPreference = "Stop"

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if (-not $existing) {
  Write-Host "Service $ServiceName is not installed."
  exit 0
}

if ($existing.Status -ne "Stopped") {
  Stop-Service -Name $ServiceName -Force
}

sc.exe delete $ServiceName | Out-Null
Write-Host "Removed service $ServiceName"
