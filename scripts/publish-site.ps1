# Publishes the committed contents of site\ to the gh-pages branch, which GitHub Pages serves.
# It never force-pushes, and it refuses to publish uncommitted or stale website files.
[CmdletBinding()]
param([string]$Remote = 'origin')
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
Push-Location $root
try {
    & node --test .\site\tests\site.test.mjs
    if ($LASTEXITCODE -ne 0) {
        throw 'Website checks failed. Rebuild the frontend, rerun node site\tests\capture.mjs, then rerun the checks.'
    }
    $changes = & git status --porcelain -- site frontend
    if ($LASTEXITCODE -ne 0 -or $changes) { throw 'Commit the website and frontend changes before publishing.' }
    $revision = & git subtree split --prefix=site --quiet HEAD
    if ($LASTEXITCODE -ne 0 -or $revision -notmatch '^[0-9a-f]{40}$') {
        throw 'Could not prepare the committed website subtree.'
    }
    & git push $Remote "${revision}:refs/heads/gh-pages"
    if ($LASTEXITCODE -ne 0) { throw 'Website push failed; the remote branch was not overwritten.' }
    Write-Host 'Website published to the gh-pages branch. GitHub Pages deploys it.'
} finally {
    Pop-Location
}
