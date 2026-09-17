$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$assets = Join-Path $root 'frontend\public\assets'
New-Item -ItemType Directory -Force $assets | Out-Null
$releasePath = Join-Path $root 'shortcut-release.json'
$signedPath = Join-Path $root 'shortcuts\Share Me.shortcut'
$destination = Join-Path $assets 'ShareMe.shortcut'
$icloudURL = ''
if (Test-Path -LiteralPath $releasePath) {
    $release = Get-Content -LiteralPath $releasePath -Raw | ConvertFrom-Json
    if ($release.schemaVersion -ne 2 -or $release.transport -ne 'ssh-v1') {
        throw 'Register the new SSH Shortcut release. Legacy HTTP Shortcut links are not compatible.'
    }
    $candidateURL = [string]$release.icloudURL
    if ($candidateURL -and $candidateURL -notmatch '^https://www\.icloud\.com/shortcuts/[a-fA-F0-9]{32}$') {
        throw 'shortcut-release.json must contain a real Apple iCloud Shortcut link.'
    }
    if ($release.physicalIPhoneVerified -is [bool] -and $release.physicalIPhoneVerified) { $icloudURL = $candidateURL }
}
$download = ''
if (Test-Path -LiteralPath $signedPath) {
    $stream = [System.IO.File]::OpenRead($signedPath)
    try {
        $header = [byte[]]::new(4)
        if ($stream.Read($header, 0, 4) -ne 4 -or [System.Text.Encoding]::ASCII.GetString($header) -ne 'AEA1') {
            throw 'Share Me.shortcut is not an Apple-signed archive. Use the publisher signing script first.'
        }
    } finally {
        $stream.Dispose()
    }
    $metadataPath = Join-Path $root 'shortcuts\ShareMe.release.json'
    if (-not (Test-Path -LiteralPath $metadataPath)) {
        throw 'Signed Shortcut release metadata is missing. Run the updated publisher script.'
    }
    $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
    $hash = (Get-FileHash -LiteralPath $signedPath -Algorithm SHA256).Hash
    if ($metadata.schemaVersion -ne 2 -or $metadata.transport -ne 'ssh-v1' -or $metadata.sha256 -ne $hash) {
        throw 'Signed Shortcut does not match the SSH release metadata.'
    }
    if ($metadata.physicalIPhoneVerified -is [bool] -and $metadata.physicalIPhoneVerified) {
        Copy-Item -LiteralPath $signedPath -Destination $destination -Force
        $download = '/assets/ShareMe.shortcut'
    }
}
if (-not $download -and (Test-Path -LiteralPath $destination)) {
    Remove-Item -LiteralPath $destination
}
$manifest = @{
    schemaVersion = 2
    transport = 'ssh-v1'
    name = 'Share Me'
    state = $(if ($icloudURL -or $download) { 'published' } else { 'unpublished' })
    url = $(if ($icloudURL) { $icloudURL } else { $download })
}
$manifest | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $assets 'shortcut-install.json') -Encoding utf8NoBOM
