param(
    [string]$Executable,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-f0-9]{40}$')]
    [string]$SourceCommit
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root
$config = Get-Content -LiteralPath 'wails.json' -Raw | ConvertFrom-Json
$version = $config.info.productVersion
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'Expected a numeric application version.' }
$name = "ShareMe-$version-windows-x64"
if (-not $Executable) { $Executable = Join-Path $root "build\bin\$name.exe" }
$Executable = (Resolve-Path -LiteralPath $Executable).Path
$go = Join-Path $root '.tools\go\bin\go.exe'
if (-not (Test-Path -LiteralPath $go)) { $go = (Get-Command go -ErrorAction Stop).Source }
$previousToolchain = $env:GOTOOLCHAIN
$stage = Join-Path $root "build\bin\$name-package"
$archive = Join-Path $root "build\bin\$name.zip"
if ((Test-Path -LiteralPath $stage) -or (Test-Path -LiteralPath $archive)) {
    throw 'Release package already exists. Use a fresh version or inspect it before rebuilding.'
}
try {
    $env:GOTOOLCHAIN = 'go1.25.14'
    $buildInfo = @(& $go version -m $Executable)
    if ($LASTEXITCODE -ne 0) { throw 'Could not read executable build information.' }
    if (-not ($buildInfo -match 'GOOS=windows') -or -not ($buildInfo -match 'GOARCH=amd64') -or
        -not ($buildInfo -match 'production') -or $buildInfo[0] -notmatch 'go1\.25\.14$') {
        throw 'Expected the production Windows x64 executable built with Go 1.25.14.'
    }
    $moduleRows = @(& $go list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all)
    if ($LASTEXITCODE -ne 0) { throw 'Could not resolve module license locations.' }
    $modules = @{}
    foreach ($row in $moduleRows) {
        $parts = $row -split '\|', 3
        $modules[$parts[0]] = $parts
    }
    $notices = [Text.StringBuilder]::new()
    [void]$notices.AppendLine('Share Me - third-party software notices')
    [void]$notices.AppendLine('This file preserves licenses for Go and modules linked into the accompanying executable.')
    $goroot = & $go env GOROOT
    if ($LASTEXITCODE -ne 0) { throw 'Could not locate the Go distribution license.' }
    [void]$notices.AppendLine("`nGo 1.25.14`nhttps://go.dev/")
    [void]$notices.AppendLine([IO.File]::ReadAllText((Join-Path $goroot 'LICENSE')))
    foreach ($line in $buildInfo) {
        if ($line -notmatch '^\s+dep\s+(\S+)\s+(\S+)') { continue }
        $module, $moduleVersion = $Matches[1], $Matches[2]
        if (-not $modules.ContainsKey($module) -or $modules[$module][1] -ne $moduleVersion) {
            throw "Linked dependency does not match the available source: $module $moduleVersion"
        }
        $licenses = @(Get-ChildItem -LiteralPath $modules[$module][2] -File |
            Where-Object { $_.Name -match '^(LICENSE|LICENCE|COPYING|NOTICE)([._-]|$)' } |
            Sort-Object Name)
        if (-not $licenses.Count) { throw "Missing redistribution notice for $module" }
        [void]$notices.AppendLine("`n========================================`n$module $moduleVersion")
        foreach ($license in $licenses) {
            [void]$notices.AppendLine("`n$($license.Name)")
            [void]$notices.AppendLine([IO.File]::ReadAllText($license.FullName))
        }
    }
    $iconNotice = [IO.File]::ReadAllText((Join-Path $root 'site\assets\NOTICE.txt'))
    $iconStart = $iconNotice.IndexOf('Screenshot and website icons')
    if ($iconStart -lt 0) { throw 'Missing icon attribution source.' }
    [void]$notices.AppendLine("`nThe application's SVG icons also use Lucide/Feather-style paths.")
    [void]$notices.AppendLine($iconNotice.Substring($iconStart))
    New-Item -ItemType Directory -Path $stage | Out-Null
    Copy-Item -LiteralPath $Executable -Destination (Join-Path $stage 'ShareMe.exe')
    [IO.File]::WriteAllText((Join-Path $stage 'THIRD-PARTY-NOTICES.txt'), $notices.ToString(), [Text.UTF8Encoding]::new($false))
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
    $exeHash = (Get-FileHash -LiteralPath $Executable -Algorithm SHA256).Hash.ToLowerInvariant()
    $metadata = [ordered]@{
        version = $version
        platform = 'windows-x64'
        preview = $true
        sourceCommit = $SourceCommit
        goVersion = '1.25.14'
        executableBytes = (Get-Item -LiteralPath $Executable).Length
        executableSHA256 = $exeHash
        signed = $false
    }
    $metadata | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $stage 'BUILD-INFO.json') -Encoding utf8NoBOM
    Compress-Archive -LiteralPath (Join-Path $stage 'ShareMe.exe'), (Join-Path $stage 'README.txt'),
        (Join-Path $stage 'THIRD-PARTY-NOTICES.txt'), (Join-Path $stage 'BUILD-INFO.json') -DestinationPath $archive
    $zipHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$archive.sha256", "$zipHash  $name.zip`n", [Text.UTF8Encoding]::new($false))
    Write-Output "Release archive: $archive"
    Write-Output "SHA-256: $zipHash"
    Write-Output "Download bytes: $((Get-Item -LiteralPath $archive).Length)"
} finally {
    $env:GOTOOLCHAIN = $previousToolchain
}
