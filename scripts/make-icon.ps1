$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
function Write-ShareMeIcon([string]$Target, [int]$Size) {
    $scale = $Size / 256
    $bitmap = [System.Drawing.Bitmap]::new($Size, $Size)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $pen = [System.Drawing.Pen]::new([System.Drawing.Color]::White, 15 * $scale)
    try {
        $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $graphics.Clear([System.Drawing.Color]::FromArgb(177, 31, 75))
        $pen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
        $pen.EndCap = [System.Drawing.Drawing2D.LineCap]::Round
        $pen.LineJoin = [System.Drawing.Drawing2D.LineJoin]::Round
        $graphics.DrawLines($pen, [System.Drawing.Point[]]@(
            [System.Drawing.Point]::new(58 * $scale, 87 * $scale),
            [System.Drawing.Point]::new(191 * $scale, 87 * $scale),
            [System.Drawing.Point]::new(153 * $scale, 49 * $scale)
        ))
        $graphics.DrawLines($pen, [System.Drawing.Point[]]@(
            [System.Drawing.Point]::new(198 * $scale, 169 * $scale),
            [System.Drawing.Point]::new(65 * $scale, 169 * $scale),
            [System.Drawing.Point]::new(103 * $scale, 207 * $scale)
        ))
        [System.IO.Directory]::CreateDirectory((Split-Path $Target -Parent)) | Out-Null
        $bitmap.Save($Target, [System.Drawing.Imaging.ImageFormat]::Png)
    } finally {
        $pen.Dispose()
        $graphics.Dispose()
        $bitmap.Dispose()
    }
}

$root = Split-Path $PSScriptRoot -Parent
Write-ShareMeIcon (Join-Path $root 'build\appicon.png') 256
Write-ShareMeIcon (Join-Path $root 'frontend\public\icons\share-me-180.png') 180
Write-ShareMeIcon (Join-Path $root 'frontend\public\icons\share-me-192.png') 192
Write-ShareMeIcon (Join-Path $root 'frontend\public\icons\share-me-512.png') 512
