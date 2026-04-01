# Bulk-delete ALL GitHub Releases (including orphaned ones with no tag) and remote v* tags.
# Prerequisites: GitHub CLI — https://cli.github.com/
#   gh auth login  (token MUST allow deleting releases):
#   - Classic PAT: scope "repo" (Full control of private repositories), OR
#   - Fine-grained: Repository -> Contents: Read and write (and metadata read).
#   If you see 403 "Resource not accessible by personal access token", create a new PAT
#   with those permissions and run: gh auth logout  then  gh auth login
#
# Usage (from repo root — one command per line):
#   Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force
#   .\scripts\cleanup-github-releases.ps1
#
# Optional: $env:GITHUB_REPO = "qlpqlp/silly-pups"

$ErrorActionPreference = "Continue"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $Root

if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
    Write-Host "Install GitHub CLI: https://cli.github.com/"
    exit 1
}

$repo = $env:GITHUB_REPO
if (-not $repo) {
    $origin = (& git remote get-url origin 2>$null)
    if ($origin -match "github\.com[:/]([^/]+)/([^/.]+)") {
        $repo = "$($Matches[1])/$($Matches[2] -replace '\.git$','')"
    }
}
if (-not $repo) {
    Write-Host "Set environment variable GITHUB_REPO=owner/repo (e.g. qlpqlp/silly-pups)"
    exit 1
}

$split = $repo -split '/', 2
if ($split.Count -ne 2) {
    Write-Host "Invalid repo format: use owner/name"
    exit 1
}
$owner, $name = $split[0], $split[1]

Write-Host "Repo: $repo"
Write-Host "This deletes every GitHub Release (by API id), including orphans with no tag, then remote v* tags."
$ok = Read-Host "Type YES to continue"
if ($ok -ne "YES") {
    Write-Host "Aborted."
    exit 1
}

# Always fetch first page and delete until none left (handles 100+ releases and avoids pagination bugs).
$maxPasses = 500
$pass = 0
while ($pass -lt $maxPasses) {
    $pass++
    $raw = & gh api "repos/$owner/$name/releases?per_page=100" 2>$null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "gh api releases failed (exit $LASTEXITCODE). Check: gh auth status"
        break
    }
    $rawStr = [string]$raw
    if ([string]::IsNullOrWhiteSpace($rawStr) -or $rawStr.Trim() -eq '[]') { break }
    try {
        $arr = $raw | ConvertFrom-Json
    } catch {
        Write-Host "Could not parse releases JSON: $_"
        break
    }
    if ($null -eq $arr) {
        Write-Host "No more releases on GitHub."
        break
    }
    $list = @($arr)
    if ($list.Count -eq 0) {
        Write-Host "No more releases on GitHub."
        break
    }
    $anyDeleteFailed = $false
    foreach ($r in $list) {
        $id = $r.id
        $tn = $r.tag_name
        Write-Host "Deleting release id=$id tag=$tn"
        $delOut = & gh api -X DELETE "repos/$owner/$name/releases/$id" 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Host $delOut
            $msg = [string]$delOut
            if ($msg -match '403|not accessible|Resource not accessible') {
                Write-Host ""
                Write-Host "Stopped: GitHub refused delete (403). Your PAT cannot manage releases."
                Write-Host "Fix: GitHub.com -> Settings -> Developer settings -> PATs"
                Write-Host "  Classic: check scope 'repo'. Fine-grained: Contents = Read and write."
                Write-Host "  Then: gh auth logout && gh auth login"
                exit 2
            }
            $anyDeleteFailed = $true
        }
    }
    if ($anyDeleteFailed) {
        Write-Host "One or more deletes failed; fix errors above before re-running (avoid infinite retry)."
        break
    }
}

if ($pass -ge $maxPasses) {
    Write-Host "Warning: stopped after $maxPasses passes; run script again if releases remain."
}

Write-Host "Removing any remaining remote tags matching v*..."
& git fetch origin --prune
$output = & git ls-remote origin 2>$null
foreach ($line in $output) {
    if ($line -notmatch "^\S+\s+(.+)$") { continue }
    $ref = $Matches[1]
    if ($ref -notmatch "^refs/tags/v") { continue }
    if ($ref -match '\^\{\}$') { continue }
    Write-Host "Deleting remote $ref"
    & git push origin ":$ref" 2>$null
}

Write-Host "Done. Local tags: git fetch --prune origin; git tag -l 'v*' | ForEach-Object { git tag -d `$_ }"
Write-Host "Then: GitHub - Actions - Release - Run workflow (e.g. v1.0.0)."
