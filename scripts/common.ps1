# Shared by build.ps1, package-release.ps1, and package-msix.ps1.

# The newest supported Go line that Wails 2 can build with. Wails 2's package loader cannot read
# Go 1.27 export data, and Go 1.25 no longer receives security fixes.
$GoToolchain = 'go1.26.8'

function Resolve-Go {
    param([string]$Root)
    $portable = Join-Path $Root '.tools\go\bin\go.exe'
    if (Test-Path -LiteralPath $portable) { return $portable }
    $command = Get-Command go -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command) { return $command.Source }
    $installed = Join-Path $env:ProgramFiles 'Go\bin\go.exe'
    if (Test-Path -LiteralPath $installed) { return $installed }
    throw 'Install Go 1.26, then rerun this script.'
}

# Returns `go version -m` output after proving the executable is the production Windows x64 build
# made with the pinned toolchain. GOTOOLCHAIN must already be set to $GoToolchain.
function Get-ProductionBuildInfo {
    param([string]$Go, [string]$Executable)
    $buildInfo = @(& $Go version -m $Executable)
    if ($LASTEXITCODE -ne 0) { throw 'Could not read executable build information.' }
    if (-not ($buildInfo -match 'GOOS=windows') -or -not ($buildInfo -match 'GOARCH=amd64') -or
        -not ($buildInfo -match 'production') -or $buildInfo[0] -notmatch "$([regex]::Escape($GoToolchain))`$") {
        throw "Expected the production Windows x64 executable built with $GoToolchain."
    }
    return $buildInfo
}

# Collects the license files of Go and of every module linked into the executable.
function Get-ThirdPartyNotices {
    param([string]$Root, [string]$Go, [string[]]$BuildInfo)
    $moduleRows = @(& $Go list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all)
    if ($LASTEXITCODE -ne 0) { throw 'Could not resolve module license locations.' }
    $modules = @{}
    foreach ($row in $moduleRows) {
        $parts = $row -split '\|', 3
        $modules[$parts[0]] = $parts
    }
    $notices = [Text.StringBuilder]::new()
    [void]$notices.AppendLine('Share Me - third-party software notices')
    [void]$notices.AppendLine('This file preserves licenses for Go and modules linked into the accompanying executable.')
    $goroot = & $Go env GOROOT
    if ($LASTEXITCODE -ne 0) { throw 'Could not locate the Go distribution license.' }
    [void]$notices.AppendLine("`nGo $($GoToolchain -replace '^go')`nhttps://go.dev/")
    [void]$notices.AppendLine([IO.File]::ReadAllText((Join-Path $goroot 'LICENSE')))
    foreach ($line in $BuildInfo) {
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
    $iconNotice = [IO.File]::ReadAllText((Join-Path $Root 'site\assets\NOTICE.txt'))
    $iconStart = $iconNotice.IndexOf('Screenshot and website icons')
    if ($iconStart -lt 0) { throw 'Missing icon attribution source.' }
    [void]$notices.AppendLine("`nThe application's SVG icons also use Lucide/Feather-style paths.")
    [void]$notices.AppendLine($iconNotice.Substring($iconStart))
    return $notices.ToString()
}

# Draws the Share Me mark at the requested size instead of scaling a bitmap, so small sizes stay sharp.
function Write-ShareMeIcon {
    param([string]$Target, [int]$Size)
    Add-Type -AssemblyName System.Drawing
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

function Get-PackagePublisherId {
    param([string]$Publisher)
    # Windows derives the package family suffix from the first 64 bits of SHA-256(UTF-16LE publisher).
    $hash = [Security.Cryptography.SHA256]::HashData([Text.Encoding]::Unicode.GetBytes($Publisher))
    $bits = (-join ($hash[0..7] | ForEach-Object { [Convert]::ToString($_, 2).PadLeft(8, '0') })) + '0'
    $alphabet = '0123456789abcdefghjkmnpqrstvwxyz'
    -join (0..12 | ForEach-Object { $alphabet[[Convert]::ToInt32($bits.Substring($_ * 5, 5), 2)] })
}

# Without a path, returns the local development identity. With a path, reads the values copied from
# Partner Center > Product identity and checks that they agree with each other.
function Get-MsixIdentity {
    param([string]$Path)
    if (-not $Path) {
        $publisher = 'CN=Burke Holland'
        return [pscustomobject]@{
            Store = $false
            IdentityName = 'BurkeHolland.ShareMe.Development'
            Publisher = $publisher
            PublisherDisplayName = 'Burke Holland'
            DisplayName = 'Share Me Development'
            PackageFamilyName = "BurkeHolland.ShareMe.Development_$(Get-PackagePublisherId $publisher)"
        }
    }
    $json = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
    $values = @{}
    foreach ($field in 'identityName', 'publisher', 'publisherDisplayName', 'displayName', 'packageFamilyName') {
        $property = $json.PSObject.Properties[$field]
        $value = if ($property) { $property.Value } else { $null }
        if ($value -isnot [string] -or -not $value.Trim() -or $value -cne $value.Trim() -or $value.Length -gt 256) {
            throw "Store identity file needs '$field' copied exactly from Partner Center Product identity."
        }
        $values[$field] = $value
    }
    if ($values.identityName -notmatch '^[A-Za-z0-9][A-Za-z0-9.-]{2,49}$' -or $values.identityName -match '\.Development$') {
        throw "Invalid Store package identity name: $($values.identityName)"
    }
    if ($values.publisher -notmatch '^CN=.+') { throw 'Store publisher must be the complete CN=... value from Partner Center.' }
    $family = "$($values.identityName)_$(Get-PackagePublisherId $values.publisher)"
    if ($family -cne $values.packageFamilyName) {
        throw "Identity name and publisher produce package family $family, not $($values.packageFamilyName). Copy both from Partner Center exactly."
    }
    [pscustomobject]@{
        Store = $true
        IdentityName = $values.identityName
        Publisher = $values.publisher
        PublisherDisplayName = $values.publisherDisplayName
        DisplayName = $values.displayName
        PackageFamilyName = $family
    }
}
