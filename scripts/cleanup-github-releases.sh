#!/usr/bin/env bash
# Bulk-delete ALL GitHub Releases (including orphaned ones with no tag) and remote v* tags.
# Prerequisites: https://cli.github.com/ — gh auth login
# Token must allow deleting releases: classic PAT with "repo", or fine-grained "Contents: Read and write".
# 403 "Resource not accessible" => create new PAT / fix scopes, then gh auth logout && gh auth login
#
# Usage (from repo root):
#   GITHUB_REPO=qlpqlp/silly-pups ./scripts/cleanup-github-releases.sh
#
# Windows: .\scripts\cleanup-github-releases.ps1
#
# Default repo is inferred from origin if GITHUB_REPO is unset.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if ! command -v gh >/dev/null 2>&1; then
  echo "Install GitHub CLI: https://cli.github.com/"
  exit 1
fi

if [[ -z "${GITHUB_REPO:-}" ]]; then
  ORIGIN="$(git remote get-url origin 2>/dev/null || true)"
  if [[ "$ORIGIN" =~ github\.com[:/]([^/]+)/([^/.]+) ]]; then
    GITHUB_REPO="${BASH_REMATCH[1]}/${BASH_REMATCH[2]%.git}"
  else
    echo "Set GITHUB_REPO=owner/repo (e.g. qlpqlp/silly-pups)"
    exit 1
  fi
fi

OWNER="${GITHUB_REPO%%/*}"
NAME="${GITHUB_REPO#*/}"

echo "Repo: $GITHUB_REPO"
echo "This deletes every GitHub Release (by API id), including orphans with no tag, then remote v* tags."
read -r -p "Type YES to continue: " ok
[[ "$ok" == "YES" ]] || { echo "Aborted."; exit 1; }

pass=0
while [[ "$pass" -lt 500 ]]; do
  pass=$((pass + 1))
  RAW="$(gh api "repos/$OWNER/$NAME/releases?per_page=100" 2>/dev/null || true)"
  [[ -z "$RAW" || "$RAW" == "[]" ]] && break
  # Portable ID list: Python (Git Bash often has it) or jq
  if command -v python3 >/dev/null 2>&1; then
    IDS="$(printf '%s' "$RAW" | python3 -c "import json,sys; d=json.load(sys.stdin); print('\n'.join(str(r['id']) for r in d))" 2>/dev/null || true)"
  elif command -v jq >/dev/null 2>&1; then
    IDS="$(printf '%s' "$RAW" | jq -r '.[].id' 2>/dev/null || true)"
  else
    echo "Need python3 or jq to parse release JSON. Install jq or Python."
    exit 1
  fi
  [[ -z "$IDS" ]] && break
  while IFS= read -r id; do
    [[ -z "$id" ]] && continue
    echo "Deleting release id=$id"
    gh api -X DELETE "repos/$OWNER/$NAME/releases/$id" || true
  done <<< "$IDS"
done

echo "Removing any remaining remote tags matching v*..."
git fetch origin --prune
while read -r _ ref; do
  [[ "$ref" =~ ^refs/tags/v ]] || continue
  [[ "$ref" == *'^{}' ]] && continue
  echo "Deleting remote $ref"
  git push origin ":$ref" || true
done < <(git ls-remote origin)

echo "Done. Local tags: git tag -l 'v*' | xargs -r git tag -d"
echo "Then: GitHub → Actions → Release → Run workflow (e.g. v1.0.0)."
