param(
  [string]$ServiceName = "FlexConnect"
)

$ErrorActionPreference = "Stop"

function Invoke-Sc {
  param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)

  & sc.exe @Arguments | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "sc.exe $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
  }
}

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if (-not $existing) {
  Write-Host "Service $ServiceName is not installed."
  exit 0
}

if ($existing.Status -ne "Stopped") {
  Stop-Service -Name $ServiceName -Force
  $existing.WaitForStatus("Stopped", [TimeSpan]::FromSeconds(30))
}

Invoke-Sc delete $ServiceName
for ($i = 0; $i -lt 30; $i++) {
  if (-not (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
    Write-Host "Removed service $ServiceName"
    exit 0
  }
  Start-Sleep -Milliseconds 500
}

throw "Timed out waiting for service $ServiceName to be removed"
