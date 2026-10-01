#Requires -Version 7.2
# Packages the production ShareMe.exe that build.ps1 produced as an MSIX and verifies the result
# by unpacking it. Without -StoreIdentity the package gets a local development identity, for
# testing under package identity. With it, the package is the unsigned Microsoft Store upload.
[CmdletBinding()]
param(
    [string]$Executable,
    [string]$StoreIdentity,
    [string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'common.ps1')
$root = Split-Path $PSScriptRoot -Parent
$version = (Get-Content -LiteralPath (Join-Path $root 'wails.json') -Raw | ConvertFrom-Json).info.productVersion
# MSIX needs a nonzero first number and the Store reserves the fourth.
if ($version -notmatch '^[1-9]\d{0,4}\.\d{1,5}\.\d{1,5}$' -or @($version.Split('.') | Where-Object { [int]$_ -gt 65535 })) {
    throw "Application version $version cannot be used as an MSIX version. Use 1.0.0 or later."
}
$packageVersion = "$version.0"
if ($StoreIdentity) { $StoreIdentity = (Resolve-Path -LiteralPath $StoreIdentity).Path }
$identity = Get-MsixIdentity $StoreIdentity
if (-not $Executable) { $Executable = Join-Path $root 'build\bin\ShareMe.exe' }
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $root 'build\bin' }
$Executable = (Resolve-Path -LiteralPath $Executable).Path
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)

$info = (Get-Item -LiteralPath $Executable).VersionInfo
$exeVersion = '{0}.{1}.{2}' -f $info.FileMajorPart, $info.FileMinorPart, $info.FileBuildPart
if ($exeVersion -ne $version) { throw "The executable is version $exeVersion, but wails.json says $version. Run scripts\build.ps1 first." }
# A plain `go build` has no connection-service address and cannot pair with a phone.
$serviceURL = (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'service-url.json') -Raw | ConvertFrom-Json).url
if (-not [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($Executable)).Contains($serviceURL)) {
    throw "The executable was not built with the connection service $serviceURL. Run scripts\build.ps1 first."
}
$go = Resolve-Go $root
$previousToolchain = $env:GOTOOLCHAIN
try {
    $env:GOTOOLCHAIN = $GoToolchain
    $buildInfo = Get-ProductionBuildInfo $go $Executable
    $notices = Get-ThirdPartyNotices $root $go $buildInfo
} finally {
    $env:GOTOOLCHAIN = $previousToolchain
}

function Find-SdkTool([string]$Name) {
    $sdkBin = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
    $tool = @(Get-ChildItem "$sdkBin\*\x64\$Name" -ErrorAction SilentlyContinue |
        Sort-Object { [version]$_.Directory.Parent.Name } -Descending | Select-Object -First 1)
    if (-not $tool) { throw "$Name was not found. Install the Windows 11 SDK." }
    $tool[0].FullName
}
function Invoke-Quiet([string]$Command, [string[]]$Arguments) {
    $output = & $Command @Arguments 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { throw "$Command failed with exit code $LASTEXITCODE.`n$output" }
}
$makeAppx = Find-SdkTool 'makeappx.exe'
$makePri = Find-SdkTool 'makepri.exe'

$flavor = if ($identity.Store) { 'store' } else { 'development' }
$stage = Join-Path $root "build\bin\msix\$flavor"
$layout = Join-Path $stage 'layout'
$priRoot = Join-Path $stage 'pri'
$verification = Join-Path $stage 'verification'
if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Force $layout, "$priRoot\Assets", $verification, $OutputDirectory | Out-Null

Copy-Item -LiteralPath $Executable -Destination (Join-Path $layout 'ShareMe.exe')
Copy-Item -LiteralPath (Join-Path $root 'PRIVACY.md') -Destination $layout
[IO.File]::WriteAllText((Join-Path $layout 'THIRD-PARTY-NOTICES.txt'), $notices, [Text.UTF8Encoding]::new($false))

$assets = [ordered]@{}
foreach ($logo in @(@{ Name = 'StoreLogo'; Size = 50 }, @{ Name = 'Square44x44Logo'; Size = 44 }, @{ Name = 'Square150x150Logo'; Size = 150 })) {
    foreach ($scale in 100, 125, 150, 200, 400) {
        $assets["$($logo.Name).scale-$scale.png"] = [int][Math]::Round($logo.Size * $scale / 100, [MidpointRounding]::AwayFromZero)
    }
}
foreach ($size in 16, 24, 32, 48, 256) {
    $assets["Square44x44Logo.targetsize-$size.png"] = $size
    $assets["Square44x44Logo.targetsize-${size}_altform-unplated.png"] = $size
}
foreach ($name in $assets.Keys) { Write-ShareMeIcon (Join-Path $priRoot "Assets\$name") $assets[$name] }
Copy-Item -LiteralPath "$priRoot\Assets" -Destination $layout -Recurse

$manifest = (Get-Content -LiteralPath (Join-Path $root 'packaging\msix\AppxManifest.xml.in') -Raw).
    Replace('{{IDENTITY_NAME}}', [Security.SecurityElement]::Escape($identity.IdentityName)).
    Replace('{{PUBLISHER}}', [Security.SecurityElement]::Escape($identity.Publisher)).
    Replace('{{PUBLISHER_DISPLAY_NAME}}', [Security.SecurityElement]::Escape($identity.PublisherDisplayName)).
    Replace('{{PRODUCT_DISPLAY_NAME}}', [Security.SecurityElement]::Escape($identity.DisplayName)).
    Replace('{{VERSION}}', $packageVersion)
if ($manifest.Contains('{{')) { throw 'The MSIX manifest template has an unknown placeholder.' }
$manifestPath = Join-Path $layout 'AppxManifest.xml'
Set-Content -LiteralPath $manifestPath -Value $manifest -Encoding utf8NoBOM -NoNewline

# Index only the icons so Windows can choose the right scale and taskbar size variants.
$priConfig = Join-Path $stage 'priconfig.xml'
@'
<?xml version="1.0" encoding="utf-8"?>
<resources targetOsVersion="10.0.0" majorVersion="1">
  <index root="\" startIndexAt="\">
    <default>
      <qualifier name="Language" value="en-US" />
      <qualifier name="Contrast" value="standard" />
      <qualifier name="Scale" value="100" />
      <qualifier name="HomeRegion" value="001" />
      <qualifier name="TargetSize" value="256" />
      <qualifier name="LayoutDirection" value="LTR" />
      <qualifier name="Theme" value="dark" />
      <qualifier name="AlternateForm" value="" />
      <qualifier name="DXFeatureLevel" value="DX9" />
      <qualifier name="Configuration" value="" />
      <qualifier name="DeviceFamily" value="Universal" />
      <qualifier name="Custom" value="" />
    </default>
    <indexer-config type="folder" foldernameAsQualifier="true" filenameAsQualifier="true" qualifierDelimiter="." />
  </index>
</resources>
'@ | Set-Content -LiteralPath $priConfig -Encoding utf8NoBOM
Invoke-Quiet $makePri @('new', '/pr', $priRoot, '/cf', $priConfig, '/mn', $manifestPath, '/of', (Join-Path $layout 'resources.pri'), '/o')
$priDump = Join-Path $stage 'resources.pri.xml'
Invoke-Quiet $makePri @('dump', '/if', (Join-Path $layout 'resources.pri'), '/of', $priDump, '/o')
[xml]$priIndex = Get-Content -LiteralPath $priDump -Raw
foreach ($logo in 'StoreLogo', 'Square44x44Logo', 'Square150x150Logo') {
    $indexed = @($assets.Keys | Where-Object { $_.StartsWith("$logo.") }).Count
    $candidates = @($priIndex.SelectNodes("//NamedResource[@name='$logo.png']/Candidate")).Count
    if ($candidates -ne $indexed) { throw "resources.pri indexes $candidates of $indexed $logo images." }
}

$package = Join-Path $OutputDirectory "ShareMe-$version-windows-x64-$flavor.msix"
Invoke-Quiet $makeAppx @('pack', '/d', $layout, '/p', $package, '/o')
Invoke-Quiet $makeAppx @('unpack', '/p', $package, '/d', $verification, '/o')

$packed = @{}
foreach ($file in Get-ChildItem -LiteralPath $verification -Recurse -File) {
    $packed[[IO.Path]::GetRelativePath($verification, $file.FullName)] = $file.FullName
}
$staged = @(Get-ChildItem -LiteralPath $layout -Recurse -File)
foreach ($file in $staged) {
    $relative = [IO.Path]::GetRelativePath($layout, $file.FullName)
    if (-not $packed.ContainsKey($relative) -or
        (Get-FileHash -LiteralPath $packed[$relative] -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash) {
        throw "MSIX content differs from the staged layout: $relative"
    }
    $packed.Remove($relative)
}
$extra = @($packed.Keys | Where-Object { $_ -notin 'AppxBlockMap.xml', '[Content_Types].xml' })
if ($extra.Count) { throw "Unexpected files in the MSIX: $($extra -join ', ')" }

[xml]$document = Get-Content -LiteralPath (Join-Path $verification 'AppxManifest.xml') -Raw
$ns = [Xml.XmlNamespaceManager]::new($document.NameTable)
$ns.AddNamespace('f', 'http://schemas.microsoft.com/appx/manifest/foundation/windows10')
$ns.AddNamespace('uap', 'http://schemas.microsoft.com/appx/manifest/uap/windows10')
$ns.AddNamespace('desktop', 'http://schemas.microsoft.com/appx/manifest/desktop/windows10')
$packageIdentity = $document.SelectSingleNode('/f:Package/f:Identity', $ns)
$application = $document.SelectSingleNode('/f:Package/f:Applications/f:Application', $ns)
$startupTask = $application.SelectSingleNode('f:Extensions/desktop:Extension/desktop:StartupTask', $ns)
$startupSource = Get-Content -LiteralPath (Join-Path $root 'startup_windows.go') -Raw
$actual = [ordered]@{
    'identity name' = $packageIdentity.GetAttribute('Name')
    'publisher' = $packageIdentity.GetAttribute('Publisher')
    'version' = $packageIdentity.GetAttribute('Version')
    'architecture' = $packageIdentity.GetAttribute('ProcessorArchitecture')
    'package display name' = $document.SelectSingleNode('/f:Package/f:Properties/f:DisplayName', $ns).InnerText
    'publisher display name' = $document.SelectSingleNode('/f:Package/f:Properties/f:PublisherDisplayName', $ns).InnerText
    'application display name' = $application.SelectSingleNode('uap:VisualElements', $ns).GetAttribute('DisplayName')
    'startup task display name' = $startupTask.GetAttribute('DisplayName')
    'startup task enabled by default' = $startupTask.GetAttribute('Enabled')
    'executable' = $application.GetAttribute('Executable')
}
$expected = [ordered]@{
    'identity name' = $identity.IdentityName
    'publisher' = $identity.Publisher
    'version' = $packageVersion
    'architecture' = 'x64'
    'package display name' = $identity.DisplayName
    'publisher display name' = $identity.PublisherDisplayName
    'application display name' = $identity.DisplayName
    'startup task display name' = $identity.DisplayName
    'startup task enabled by default' = 'false'
    'executable' = 'ShareMe.exe'
}
foreach ($key in $expected.Keys) {
    if ($actual[$key] -cne $expected[$key]) { throw "Packaged manifest $key is '$($actual[$key])', expected '$($expected[$key])'." }
}
if (-not $startupSource.Contains("startupTaskID = `"$($startupTask.GetAttribute('TaskId'))`"")) {
    throw 'The manifest startup TaskId differs from startupTaskID in startup_windows.go.'
}
$capabilities = @($document.SelectNodes('/f:Package/f:Capabilities/*', $ns) | ForEach-Object { $_.GetAttribute('Name') })
if (($capabilities -join ',') -ne 'internetClient,privateNetworkClientServer,runFullTrust') {
    throw "Unexpected MSIX capabilities: $($capabilities -join ', ')"
}

$receipt = [ordered]@{
    applicationVersion = $version
    packageVersion = $packageVersion
    storeIdentity = $identity.Store
    identityName = $identity.IdentityName
    publisher = $identity.Publisher
    publisherDisplayName = $identity.PublisherDisplayName
    displayName = $identity.DisplayName
    packageFamilyName = $identity.PackageFamilyName
    architecture = 'x64'
    minimumWindowsVersion = $document.SelectSingleNode('//f:TargetDeviceFamily', $ns).GetAttribute('MinVersion')
    capabilities = $capabilities
    startupTaskId = $startupTask.GetAttribute('TaskId')
    goVersion = $GoToolchain -replace '^go'
    signed = $false
    package = $package
    packageSha256 = (Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash.ToLowerInvariant()
    executableSha256 = (Get-FileHash -LiteralPath $Executable -Algorithm SHA256).Hash.ToLowerInvariant()
    fileCount = $staged.Count
    verifiedByUnpack = $true
}
$receipt | ConvertTo-Json | Set-Content -LiteralPath "$package.json" -Encoding utf8NoBOM
Write-Host "MSIX package verified by unpacking: $package"
Write-Host "MSIX version $packageVersion, identity $($identity.IdentityName), unsigned, SHA-256 $($receipt.packageSha256)"
if (-not $identity.Store) { Write-Warning 'This package uses the development identity. Pass -StoreIdentity for a Microsoft Store upload.' }
[pscustomobject]$receipt
