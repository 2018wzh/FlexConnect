param(
  [string]$OutputDir = "dist\windows"
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

New-Item -ItemType Directory -Force $OutputDir | Out-Null

$flexd = Join-Path $OutputDir "flexconnectd.exe"
$flexctl = Join-Path $OutputDir "flexconnect.exe"
$flextray = Join-Path $OutputDir "flextray.exe"
$trayIcon = Join-Path $root "assets\icons\app-256.png"
$trayPackageDir = Join-Path $root "cmd\flextray"
$traySyso = Join-Path $trayPackageDir "flextray_windows_amd64.syso"

if (-not (Test-Path $trayIcon)) {
  throw "Missing tray icon source at $trayIcon. Run .\\scripts\\generate-icons.ps1 first."
}

Push-Location $trayPackageDir
try {
  if (Test-Path $traySyso) {
    Remove-Item $traySyso -Force
  }
  go run github.com/tc-hib/go-winres@latest simply `
    --icon $trayIcon `
    --arch amd64 `
    --out flextray `
    --manifest gui `
    --product-name "FlexConnect" `
    --file-description "FlexConnect tray" `
    --original-filename "flextray.exe"
} finally {
  Pop-Location
}

go build -o $flexd .\cmd\flexconnectd
$dllSrc = Join-Path $root "assets\windows\wintun.dll"
$dllDst = Join-Path $OutputDir "wintun.dll"
Copy-Item $dllSrc $dllDst -Force

go build -o $flexctl .\cmd\flexconnect
go build -ldflags "-H=windowsgui" -o $flextray .\cmd\flextray

Write-Host "Built artifacts in $OutputDir"
