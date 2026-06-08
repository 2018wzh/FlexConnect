param(
  [string]$BinDir = "dist\windows",
  [string]$ServiceName = "FlexConnect",
  [string]$SocketPath = "",
  [string]$StatePath = ""
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

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
  sc.exe config $ServiceName binPath= $binPath start= auto | Out-Null
} else {
  New-Service -Name $ServiceName -BinaryPathName $binPath -DisplayName $ServiceName -StartupType Automatic | Out-Null
}

New-Item -ItemType Directory -Force (Split-Path -Parent $StatePath) | Out-Null
Start-Service -Name $ServiceName

Write-Host "Installed and started service $ServiceName"
Write-Host "Daemon: $flexd"
Write-Host "State:  $StatePath"
Write-Host "Socket: $SocketPath"
