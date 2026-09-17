param(
    [Parameter(Mandatory = $true, ParameterSetName = 'iCloud')]
    [string]$ICloudURL,
    [Parameter(Mandatory = $true, ParameterSetName = 'File')]
    [switch]$SignedArtifact,
    [switch]$VerifiedOnIPhone
)
$ErrorActionPreference = 'Stop'
if (-not $VerifiedOnIPhone) {
    throw 'Complete the fresh-iPhone checklist first, including setup without editing actions, then pass -VerifiedOnIPhone.'
}
$root = Split-Path $PSScriptRoot -Parent
if ($SignedArtifact) {
    $metadataPath = Join-Path $root 'shortcuts\ShareMe.release.json'
    $signedPath = Join-Path $root 'shortcuts\Share Me.shortcut'
    $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
    $hash = (Get-FileHash -LiteralPath $signedPath -Algorithm SHA256).Hash
    if ($metadata.schemaVersion -ne 2 -or $metadata.transport -ne 'ssh-v1' -or $metadata.sha256 -ne $hash) {
        throw 'The signed file does not match its SSH release metadata.'
    }
    $metadata | Add-Member -NotePropertyName physicalIPhoneVerified -NotePropertyValue $true -Force
    $metadata | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $metadataPath -Encoding utf8NoBOM
} else {
    if ($ICloudURL -notmatch '^https://www\.icloud\.com/shortcuts/[a-fA-F0-9]{32}$') {
        throw 'Paste the real link from Shortcuts > Share > Copy iCloud Link.'
    }
    @{ schemaVersion = 2; transport = 'ssh-v1'; icloudURL = $ICloudURL; physicalIPhoneVerified = $true } |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $root 'shortcut-release.json') -Encoding utf8NoBOM
}
Write-Host 'Publisher verification recorded. Rebuild and deploy the hosted assets to publish.'
