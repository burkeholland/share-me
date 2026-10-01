param(
    [string]$ServiceURL,
    [string]$OutputName = 'ShareMe.exe'
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'common.ps1')
Set-Location (Split-Path $PSScriptRoot -Parent)
$env:PATH = "$(Split-Path (Resolve-Go $PWD) -Parent);$env:PATH"
if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
    throw 'Install Node.js 22.12 or newer, then rerun scripts\build.ps1.'
}
& (Join-Path $PSScriptRoot 'make-icon.ps1')
$previousToolchain = $env:GOTOOLCHAIN
try {
    $env:GOTOOLCHAIN = $GoToolchain
    if (-not $ServiceURL) {
        $config = Join-Path $PSScriptRoot 'service-url.json'
        if (Test-Path $config) { $ServiceURL = (Get-Content $config -Raw | ConvertFrom-Json).url }
    }
    $url = $null
    if (-not [Uri]::TryCreate($ServiceURL, [UriKind]::Absolute, [ref]$url) -or $url.Scheme -ne 'https' -or
        $url.UserInfo -or $url.Query -or $url.Fragment -or $url.AbsolutePath -ne '/') {
        throw 'Provide -ServiceURL with the deployed HTTPS origin. No insecure fallback will be enabled.'
    }
    $origin = $url.GetLeftPart([UriPartial]::Authority)
    Push-Location frontend
    try {
        npm run build
        if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed.' }
    } finally { Pop-Location }
    go run github.com/wailsapp/wails/v2/cmd/wails@v2.12.0 build -skipbindings -s -trimpath -webview2 error -platform windows/amd64 -o $OutputName -ldflags "-X main.defaultServiceURL=$origin"
    if ($LASTEXITCODE -ne 0) { throw 'Wails build failed.' }
} finally {
    $env:GOTOOLCHAIN = $previousToolchain
}
Write-Host "Executable: $PWD\build\bin\$OutputName"
