[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$publisher = Join-Path $PSScriptRoot 'Publish-Shortcut.command'
$source = [IO.File]::ReadAllText($publisher)
$raw = [IO.File]::ReadAllBytes($publisher)
if ($source.Contains("`r") -or $raw.Length -lt 2 -or $raw[0] -ne 35 -or $raw[1] -ne 33) {
    throw 'The Mac publisher must use LF line endings and a BOM-free shebang.'
}
& bash -n $publisher
if ($LASTEXITCODE -ne 0) { throw 'Publisher shell syntax failed.' }
foreach ($required in @(
    'if [[ "$(uname -s)" != "Darwin" ]]',
    'output="$directory/Share Me.shortcut"',
    'shortcuts sign --mode anyone --input "$work/Share Me.shortcut" --output "$work/signed/Share Me.shortcut"',
    'signed="$work/signed/Share Me.shortcut"',
    'signed_sha="$(shasum -a 256 "$signed"',
    'mv -f -- "$signed" "$output"',
    'mv -f -- "$work/Share Me.shortcut.sha256" "$output.sha256"',
    'mv -f -- "$work/ShareMe.release.json" "$directory/ShareMe.release.json"'
)) {
    if (-not $source.Contains($required)) { throw "Publisher contract missing: $required" }
}
if ($source.Contains('ShareMe.shortcut')) { throw 'Publisher must preserve the space in the signed filename everywhere.' }

$lines = @(Get-Content -LiteralPath $publisher)
$metadataLines = @($lines | Where-Object { $_.StartsWith('printf ''{') })
$checksumLines = @($lines | Where-Object { $_.StartsWith('printf ''%s  ') })
if ($metadataLines.Count -ne 1 -or $checksumLines.Count -ne 1) {
    throw 'Expected one release metadata formatter and one checksum formatter.'
}
$metadataSuffix = ' > "$work/ShareMe.release.json"'
$checksumSuffix = ' > "$work/Share Me.shortcut.sha256"'
if (-not $metadataLines[0].EndsWith($metadataSuffix) -or -not $checksumLines[0].EndsWith($checksumSuffix)) {
    throw 'Wrong metadata or checksum sidecar destination.'
}
$metadataFormatter = $metadataLines[0].Substring(0, $metadataLines[0].Length - $metadataSuffix.Length)
$checksumFormatter = $checksumLines[0].Substring(0, $checksumLines[0].Length - $checksumSuffix.Length)

# Exercise only the production printf statements with fixture digests, not signing.
$unsignedHash = 'a' * 64
$signedHash = 'b' * 64
$variables = "unsigned_sha='$unsignedHash'`nsigned_sha='$signedHash'`npublished_at='2026-09-16T00:00:00Z'`n"
$metadataJSON = (($variables + $metadataFormatter + "`n") | & bash) -join "`n"
if ($LASTEXITCODE -ne 0) { throw 'Release metadata formatter failed.' }
$metadata = $metadataJSON | ConvertFrom-Json
if ($metadata.schemaVersion -ne 2 -or $metadata.transport -cne 'ssh-v1' -or
    $metadata.sha256 -cne $signedHash -or $metadata.unsignedSha256 -cne $unsignedHash -or
    $metadata.artifact -cne 'Share Me.shortcut' -or $metadata.downloadFilename -cne 'Share Me.shortcut' -or
    $metadata.shortcutName -cne 'Share Me' -or $metadata.signatureContainerHeaderHex -cne '41454131' -or
    $metadata.physicalIPhoneVerified -ne $false -or $null -ne $metadata.installationURL) {
    throw 'Rendered release metadata does not match the parent contract or device-validation gate.'
}
$checksum = (($variables + $checksumFormatter + "`n") | & bash) -join "`n"
if ($LASTEXITCODE -ne 0 -or $checksum -cne "$signedHash  Share Me.shortcut") {
    throw 'Rendered checksum must hash the signed artifact and preserve its spaced filename.'
}
Write-Host 'PASS: publisher syntax, canonical signed filename, schema-v2 ssh-v1 metadata and checksum formatting. No signing, release files or installation links were produced.'
