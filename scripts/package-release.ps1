param(
    [string]$Executable,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-f0-9]{40}$')]
    [string]$SourceCommit
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'common.ps1')
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root
$config = Get-Content -LiteralPath 'wails.json' -Raw | ConvertFrom-Json
$version = $config.info.productVersion
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'Expected a numeric application version.' }
$name = "ShareMe-$version-windows-x64"
if (-not $Executable) { $Executable = Join-Path $root "build\bin\$name.exe" }
$Executable = (Resolve-Path -LiteralPath $Executable).Path
$go = Resolve-Go $root
$previousToolchain = $env:GOTOOLCHAIN
$stage = Join-Path $root "build\bin\$name-package"
$archive = Join-Path $root "build\bin\$name.zip"
if ((Test-Path -LiteralPath $stage) -or (Test-Path -LiteralPath $archive)) {
    throw 'Release package already exists. Use a fresh version or inspect it before rebuilding.'
}
try {
    $env:GOTOOLCHAIN = $GoToolchain
    New-Item -ItemType Directory -Path $stage | Out-Null
    # Copy the executable once. The checks, the build record, and the archive all use this copy.
    $stagedExecutable = Join-Path $stage 'ShareMe.exe'
    Copy-Item -LiteralPath $Executable -Destination $stagedExecutable
    try {
        $buildInfo = Get-ProductionBuildInfo $go $stagedExecutable
        $notices = Get-ThirdPartyNotices $root $go $buildInfo
    } catch {
        Remove-Item -LiteralPath $stage -Recurse -Force
        throw
    }
    [IO.File]::WriteAllText((Join-Path $stage 'THIRD-PARTY-NOTICES.txt'), $notices, [Text.UTF8Encoding]::new($false))
    $readme = @"
Share Me $version - Windows x64 preview

Extract this ZIP into a folder and run ShareMe.exe. Keep the included notices.
Requires Windows and the installed Microsoft Edge WebView2 Runtime.
The executable is unsigned. Do not bypass Windows trust warnings.

Use
1. Keep Windows and the iPhone on the same trusted network.
2. Scan the QR with the iPhone Camera app and open it in Safari.
3. Click Connect on Windows to pair, then accept each incoming transfer.
4. To send from Windows, select Send, choose the phone, and offer files or text.

Close hides the app in the system tray. Right-click the tray icon and choose
Quit to stop receiving. Startup settings are off by default.

Files and text are encrypted and transferred directly over the LAN. This preview
still loads the phone page and establishes connections through Cloudflare.
It is not the planned offline, local-HTTPS version. Internet access is required
to connect. Physical-iPhone compatibility still needs final verification.

Files are checked and scanned with Windows Defender before being saved.
No scanner guarantees detection of every threat. Only accept files you expect.

Source: https://github.com/burkeholland/share-me/tree/$SourceCommit
Instructions: https://github.com/burkeholland/share-me#use
"@
    [IO.File]::WriteAllText((Join-Path $stage 'README.txt'), $readme, [Text.UTF8Encoding]::new($false))
    $exeHash = (Get-FileHash -LiteralPath $stagedExecutable -Algorithm SHA256).Hash.ToLowerInvariant()
    $metadata = [ordered]@{
        version = $version
        platform = 'windows-x64'
        preview = $true
        sourceCommit = $SourceCommit
        goVersion = $GoToolchain -replace '^go'
        executableBytes = (Get-Item -LiteralPath $stagedExecutable).Length
        executableSHA256 = $exeHash
        signed = $false
    }
    $metadata | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $stage 'BUILD-INFO.json') -Encoding utf8NoBOM
    Compress-Archive -LiteralPath $stagedExecutable, (Join-Path $stage 'README.txt'),
        (Join-Path $stage 'THIRD-PARTY-NOTICES.txt'), (Join-Path $stage 'BUILD-INFO.json') -DestinationPath $archive
    $zipHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$archive.sha256", "$zipHash  $name.zip`n", [Text.UTF8Encoding]::new($false))
    Write-Output "Release archive: $archive"
    Write-Output "SHA-256: $zipHash"
    Write-Output "Download bytes: $((Get-Item -LiteralPath $archive).Length)"
} finally {
    $env:GOTOOLCHAIN = $previousToolchain
}
