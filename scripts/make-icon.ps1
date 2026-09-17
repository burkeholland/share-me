$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
$target = Join-Path (Split-Path $PSScriptRoot -Parent) 'build\appicon.png'
$bitmap = [System.Drawing.Bitmap]::new(256, 256)
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
$pen = [System.Drawing.Pen]::new([System.Drawing.Color]::White, 15)
try {
    $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $graphics.Clear([System.Drawing.Color]::FromArgb(177, 31, 75))
    $pen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
    $pen.EndCap = [System.Drawing.Drawing2D.LineCap]::Round
    $pen.LineJoin = [System.Drawing.Drawing2D.LineJoin]::Round
    $graphics.DrawLines($pen, [System.Drawing.Point[]]@(
        [System.Drawing.Point]::new(58, 87),
        [System.Drawing.Point]::new(191, 87),
        [System.Drawing.Point]::new(153, 49)
    ))
    $graphics.DrawLines($pen, [System.Drawing.Point[]]@(
        [System.Drawing.Point]::new(198, 169),
        [System.Drawing.Point]::new(65, 169),
        [System.Drawing.Point]::new(103, 207)
    ))
    [System.IO.Directory]::CreateDirectory((Split-Path $target -Parent)) | Out-Null
    $bitmap.Save($target, [System.Drawing.Imaging.ImageFormat]::Png)
} finally {
    $pen.Dispose()
    $graphics.Dispose()
    $bitmap.Dispose()
}
