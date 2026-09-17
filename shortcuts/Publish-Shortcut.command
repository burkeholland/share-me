#!/bin/bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  printf '%s\n' 'BLOCKED: publishing requires Apple Shortcuts on macOS.' >&2
  exit 1
fi
for tool in shortcuts pwsh plutil shasum; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'BLOCKED: publisher prerequisite is missing: %s\n' "$tool" >&2
    exit 1
  fi
done

directory="$(cd -- "$(dirname -- "$0")" && pwd)"
cd -- "$directory"
input="$directory/ShareMe.unsigned.shortcut"
output="$directory/Share Me.shortcut"
if [[ ! -f "$input" || ! -f "$input.sha256" || ! -f "$directory/ShareMe.build.json" ]]; then
  printf '%s\n' 'Run Build-Shortcut.ps1 first; unsigned artifact, hash and build metadata are required.' >&2
  exit 1
fi
plutil -lint "$input"
shasum -a 256 --check "$input.sha256"
pwsh -NoLogo -NoProfile -File "$directory/Test-Shortcut.ps1" -Path "$input" -RequireBuildMetadata

# A fixed, exclusively-created staging directory avoids clobbering another publisher.
# Canonical basenames are a precaution; the imported title still needs device verification.
work="$directory/.publish-work"
if ! mkdir -- "$work"; then
  printf '%s\n' 'Publishing workspace exists; another publish may be active. Inspect it before retrying.' >&2
  exit 1
fi
cleanup() {
  rm -f -- "$work/Share Me.shortcut" "$work/ShareMe.release.json" "$work/Share Me.shortcut.sha256"
  rm -f -- "$work/signed/Share Me.shortcut"
  rmdir -- "$work/signed" 2>/dev/null || true
  rmdir -- "$work" 2>/dev/null || true
}
trap cleanup EXIT
mkdir -- "$work/signed"
cp -- "$input" "$work/Share Me.shortcut"
printf '%s\n' 'Apple will receive the generic, credential-free workflow for signing. No third-party signer is used.'
shortcuts sign --mode anyone --input "$work/Share Me.shortcut" --output "$work/signed/Share Me.shortcut"
signed="$work/signed/Share Me.shortcut"
header="$(LC_ALL=C od -An -tx1 -N4 "$signed" | tr -d ' \n')"
if [[ ! -s "$signed" || "$header" != "41454131" ]]; then
  printf '%s\n' 'Apple signing did not produce an AEA1 signature container; existing release was preserved.' >&2
  exit 1
fi
unsigned_sha="$(shasum -a 256 "$input" | cut -d ' ' -f 1)"
signed_sha="$(shasum -a 256 "$signed" | cut -d ' ' -f 1)"
published_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
printf '%s  Share Me.shortcut\n' "$signed_sha" > "$work/Share Me.shortcut.sha256"
printf '{\n  "schemaVersion": 2,\n  "shortcutName": "Share Me",\n  "transport": "ssh-v1",\n  "artifact": "Share Me.shortcut",\n  "downloadFilename": "Share Me.shortcut",\n  "unsignedSha256": "%s",\n  "sha256": "%s",\n  "signatureContainer": "AEA1",\n  "signatureContainerHeaderHex": "41454131",\n  "signedBy": "Apple shortcuts sign --mode anyone",\n  "signedAt": "%s",\n  "appleSigned": true,\n  "physicalIPhoneVerified": false,\n  "installationURL": null\n}\n' "$unsigned_sha" "$signed_sha" "$published_at" > "$work/ShareMe.release.json"

# Preserve the unsigned artifact; replace an existing signed release only after signing/checks succeed.
mv -f -- "$signed" "$output"
mv -f -- "$work/Share Me.shortcut.sha256" "$output.sha256"
mv -f -- "$work/ShareMe.release.json" "$directory/ShareMe.release.json"
printf 'Signed generic artifact: %s\n' "$output"
printf '%s\n' 'Use download filename "Share Me.shortcut". AEA1 checks the container header, not the cryptographic signature.'
printf '%s\n' 'BLOCKED for customer release until physical iPhone validation. No installation/iCloud link was created.'
