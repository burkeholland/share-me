$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'common.ps1')
$root = Split-Path $PSScriptRoot -Parent
Write-ShareMeIcon (Join-Path $root 'build\appicon.png') 256
Write-ShareMeIcon (Join-Path $root 'frontend\public\icons\share-me-180.png') 180
Write-ShareMeIcon (Join-Path $root 'frontend\public\icons\share-me-192.png') 192
Write-ShareMeIcon (Join-Path $root 'frontend\public\icons\share-me-512.png') 512
