param(
  [string]$BinDir = "dist\windows",
  [string]$ServiceName = "FlexConnect",
  [string]$SocketPath = "",
  [string]$StatePath = ""
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Invoke-Sc {
  param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)

  & sc.exe @Arguments | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "sc.exe $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
  }
}

$flexd = Join-Path $root $BinDir
$flexd = Join-Path $flexd "flexconnectd.exe"
if (-not (Test-Path $flexd)) {
  throw "Missing daemon binary: $flexd. Run .\scripts\build-windows.ps1 first."
}

if (-not $SocketPath) {
  $SocketPath = "\\.\pipe\ProtectedPrefix\Administrators\FlexConnect\flexconnectd"
}
if (-not $StatePath) {
  $StatePath = Join-Path $env:ProgramData "FlexConnect\state.json"
}

$quotedDaemon = '"' + $flexd + '"'
$binPath = "$quotedDaemon --socket `"$SocketPath`" --state `"$StatePath`""

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing) {
  Invoke-Sc config $ServiceName binPath= $binPath start= auto depend= Dnscache/iphlpsvc/netprofm/WinHttpAutoProxySvc
} else {
  New-Service -Name $ServiceName -BinaryPathName $binPath -DisplayName $ServiceName -StartupType Automatic | Out-Null
  Invoke-Sc config $ServiceName depend= Dnscache/iphlpsvc/netprofm/WinHttpAutoProxySvc
}

Invoke-Sc description $ServiceName "FlexConnect privileged local VPN daemon"
Invoke-Sc failure $ServiceName reset= 60 actions= restart/1000/restart/2000/restart/4000/restart/9000/restart/16000/restart/25000/restart/36000/restart/49000/restart/64000
Invoke-Sc failureflag $ServiceName 1

New-Item -ItemType Directory -Force (Split-Path -Parent $StatePath) | Out-Null
Start-Service -Name $ServiceName

Write-Host "Installed and started service $ServiceName"
Write-Host "Daemon: $flexd"
Write-Host "State:  $StatePath"
Write-Host "Socket: $SocketPath"
