param(
  [string]$Version = "0.1.0",
  [string]$OutputDir = "dist\\windows-package",
  [string]$UpgradeCode = "8F4A7D5B-4021-4A65-9F08-53B0E2A1C4B7"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$buildDir = Join-Path $OutputDir "payload"
.\scripts\build-windows.ps1 -OutputDir $buildDir

$zipPath = Join-Path $OutputDir ("flexconnect_" + $Version + "_windows_amd64.zip")
New-Item -ItemType Directory -Force $OutputDir | Out-Null
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
Compress-Archive -Path (Join-Path $buildDir "*") -DestinationPath $zipPath -Force

$wix = Get-Command wix -ErrorAction SilentlyContinue
if (-not $wix) {
  Write-Host "WiX not found; created ZIP only at $zipPath"
  exit 0
}

$versionParts = $Version.Split(".")
while ($versionParts.Count -lt 3) { $versionParts += "0" }
$msiVersion = ($versionParts[0..2] -join ".")
$wxsPath = Join-Path $OutputDir "flexconnect.wxs"
$msiPath = Join-Path $OutputDir ("flexconnect_" + $Version + "_windows_amd64.msi")
$iconPath = Resolve-Path (Join-Path $root "assets\\windows\\flextray.ico")

@"
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="FlexConnect" Manufacturer="FlexConnect" Version="$msiVersion" UpgradeCode="$UpgradeCode">
    <MajorUpgrade DowngradeErrorMessage="A newer version of FlexConnect is already installed." />
    <MediaTemplate />
    <Icon Id="FlexIcon.ico" SourceFile="$iconPath" />
    <Property Id="ARPPRODUCTICON" Value="FlexIcon.ico" />
    <Feature Id="MainFeature" Title="FlexConnect" Level="1">
      <ComponentGroupRef Id="ProductComponents" />
    </Feature>
  </Package>
  <Fragment>
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLDIR" Name="FlexConnect">
        <Component Id="cmpFlexConnectd" Guid="*">
          <File Source="$(Resolve-Path "$buildDir\\flexconnectd.exe")" KeyPath="yes" />
          <ServiceInstall Id="svcFlexConnectd" Name="FlexConnectD" DisplayName="FlexConnectD" Description="FlexConnect daemon" Start="auto" Type="ownProcess" ErrorControl="normal" />
          <ServiceControl Id="ctlFlexConnectd" Name="FlexConnectD" Start="install" Stop="both" Remove="uninstall" Wait="yes" />
        </Component>
        <Component Id="cmpFlexConnect" Guid="*">
          <File Source="$(Resolve-Path "$buildDir\\flexconnect.exe")" KeyPath="yes" />
        </Component>
        <Component Id="cmpFlexTray" Guid="*">
          <File Source="$(Resolve-Path "$buildDir\\flextray.exe")" KeyPath="yes" />
        </Component>
        <Component Id="cmpWintun" Guid="*">
          <File Source="$(Resolve-Path "$buildDir\\wintun.dll")" KeyPath="yes" />
        </Component>
      </Directory>
    </StandardDirectory>
  </Fragment>
  <Fragment>
    <ComponentGroup Id="ProductComponents">
      <ComponentRef Id="cmpFlexConnectd" />
      <ComponentRef Id="cmpFlexConnect" />
      <ComponentRef Id="cmpFlexTray" />
      <ComponentRef Id="cmpWintun" />
    </ComponentGroup>
  </Fragment>
</Wix>
"@ | Set-Content -Path $wxsPath -Encoding UTF8

wix build $wxsPath -o $msiPath
Write-Host "Built ZIP: $zipPath"
Write-Host "Built MSI: $msiPath"
