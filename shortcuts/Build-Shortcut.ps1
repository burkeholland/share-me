[CmdletBinding()]
param(
    [string] $GoPath = (Join-Path $PSScriptRoot '..\.tools\go\bin\go.exe')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$revision = 'dc82114f346f77e5cf04987cb0ee4e781302fc32'
$tools = Join-Path $PSScriptRoot '.tools'
$work = Join-Path $tools 'work'
$compiler = Join-Path $tools "bin\cherri-$revision.exe"
$source = Join-Path $PSScriptRoot 'ShareMe.cherri'
$output = Join-Path $PSScriptRoot 'ShareMe.unsigned.shortcut'

if (-not (Test-Path -LiteralPath $GoPath -PathType Leaf)) {
    throw "Go was not found at '$GoPath'. Pass -GoPath with a Go 1.24+ executable."
}
$GoPath = (Resolve-Path -LiteralPath $GoPath).Path
New-Item -ItemType Directory -Force -Path $work, (Split-Path $compiler), (Join-Path $tools 'home') | Out-Null
$environment = @{}
foreach ($name in @('GOBIN', 'GOPATH', 'GOCACHE', 'GOTOOLCHAIN', 'HOME')) {
    $environment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

try {
    $env:GOBIN = Split-Path $compiler
    $env:GOPATH = Join-Path $tools 'gopath'
    $env:GOCACHE = Join-Path $tools 'gocache'
    $env:GOTOOLCHAIN = 'local'
    $env:HOME = Join-Path $tools 'home'
    if (-not (Test-Path -LiteralPath $compiler -PathType Leaf)) {
        Write-Host "Building pinned Cherri $revision. Downloads compiler dependencies, never Shortcut data."
        & $GoPath install "github.com/electrikmilk/cherri@$revision"
        if ($LASTEXITCODE -ne 0) { throw 'Building the pinned Cherri compiler failed.' }
        Move-Item -LiteralPath (Join-Path $env:GOBIN 'cherri.exe') -Destination $compiler -Force
    }

    $sourceText = [IO.File]::ReadAllText($source).Replace("`r`n", "`n")
    [IO.File]::WriteAllText((Join-Path $work 'ShareMe.cherri'), $sourceText, [Text.UTF8Encoding]::new($false))
    Push-Location $work
    try {
        # Omitting --skip-sign on Windows sends the workflow to a third-party signer.
        & $compiler 'ShareMe.cherri' '--skip-sign' '--no-ansi'
        if ($LASTEXITCODE -ne 0) { throw 'Unsigned Shortcut compilation failed.' }
    } finally {
        Pop-Location
    }

    $candidate = Join-Path $work 'ShareMe.final.shortcut'
    & (Join-Path $PSScriptRoot 'Finalize-Unsigned.ps1') `
        -InputPath (Join-Path $work 'Share Me_unsigned.shortcut') -OutputPath $candidate
    & (Join-Path $PSScriptRoot 'Test-Shortcut.ps1') -Path $candidate
    Move-Item -LiteralPath $candidate -Destination $output -Force
    $digest = (Get-FileHash -LiteralPath $output -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$output.sha256", "$digest  ShareMe.unsigned.shortcut`n", [Text.UTF8Encoding]::new($false))
    $metadata = [ordered]@{
        schemaVersion = 1
        shortcutName = 'Share Me'
        transport = 'ssh-v1'
        setupPrefix = 'shareme-setup-v1:'
        protocolPort = 49322
        compiler = "github.com/electrikmilk/cherri@$revision"
        compilerFlags = @('--skip-sign', '--no-ansi')
        sourceSha256 = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant()
        unsignedArtifact = 'ShareMe.unsigned.shortcut'
        unsignedSha256 = $digest
        staticValidation = 'passed'
        appleSigned = $false
        physicalIPhoneVerified = $false
        installationURL = $null
    }
    [IO.File]::WriteAllText((Join-Path $PSScriptRoot 'ShareMe.build.json'), ($metadata | ConvertTo-Json -Depth 4) + "`n", [Text.UTF8Encoding]::new($false))
    Remove-Item -LiteralPath (Join-Path $work 'ShareMe.cherri'), (Join-Path $work 'Share Me_unsigned.shortcut')
    Write-Host "Unsigned artifact: $output"
    Write-Host "Transport: ssh-v1; name: Share Me; SHA-256: $digest"
    Write-Host 'BLOCKED: Apple publisher signing and physical iPhone verification have not been performed.'
} finally {
    foreach ($name in $environment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $environment[$name], 'Process')
    }
}
