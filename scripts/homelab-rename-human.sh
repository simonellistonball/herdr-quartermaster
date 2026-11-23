#!/usr/bin/env bash
# Homelab fork: the human decider is SEB, not upstream's TJ (the author's
# initials). That covers what agents read (briefs, the coordinator guide,
# prompts, notes) and the identity itself: `--by SEB` is the human's go-ahead
# that autonomy checks, dispatch, resume, answer and plan approve key on.
#
# The rename is mechanical and touches ~150 lines across upstream's files, so
# it lives in its own commit made by this script. On a refresh from upstream,
# drop that commit, rebase the others, and rerun this script:
#   scripts/homelab-rename-human.sh && go test ./... && git commit -am "Rename the human decider TJ to SEB"
set -euo pipefail
cd "$(dirname "$0")/.."
NEW="${1:-SEB}"
git grep -l -w 'TJ' -- '*.go' '*.md' '*.toml' '*.mjs' '*.ts' '*.sh' ':!scripts/homelab-rename-human.sh' |
  while IFS= read -r f; do
    perl -pi -e "s/\\bTJ\\b/$NEW/g" "$f"
  done
left="$(git grep -n -w 'TJ' -- ':!*.png' ':!*.gif' ':!*.mp4' ':!scripts/homelab-rename-human.sh' || true)"
[ -z "$left" ] || { echo "still names TJ:"; echo "$left"; exit 1; }
