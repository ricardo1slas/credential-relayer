#!/usr/bin/env bash
# Build a scrubbed copy of main for the public repository, verify it, and optionally push it.
#   scripts/publish.sh           build + verify only (prints where the export is)
#   scripts/publish.sh --push    ...then push it to $PUBLIC_REMOTE
# env (see local.mk.example):
#   PUBLISH_REPLACEMENTS  git filter-repo --replace-text rules (text==>replacement, one per line). It
#                         lists what must not be published, so it lives outside the repository.
#   PUBLISH_MAILMAP       optional git mailmap, e.g. to publish commits under a no-reply email
#   PUBLIC_REMOTE         git URL of the public repository (needed for --push)
# The rewrite is deterministic: the same commits and the same rules give the same hashes, so each
# publish fast-forwards the public repo. Changing the rules rewrites old commits too; the push is then
# refused rather than forced.
set -euo pipefail
cd "$(dirname "$0")/.." || exit 1
expand() { echo "${1/#\~/$HOME}"; }
RULES=$(expand "${PUBLISH_REPLACEMENTS:?set PUBLISH_REPLACEMENTS (see local.mk.example)}")
[ -r "$RULES" ] || { echo "cannot read $RULES"; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "working tree not clean: the export is built from committed main"; exit 1; }

OUT=$(mktemp -d)/export
git clone -q --no-local --single-branch --branch main . "$OUT"
args=(--replace-text "$RULES" --replace-message "$RULES" --force --quiet)
if [ -n "${PUBLISH_MAILMAP:-}" ]; then args+=(--mailmap "$(expand "$PUBLISH_MAILMAP")"); fi
(cd "$OUT" && git filter-repo "${args[@]}")

# Verify: no scrubbed value survives in any file version, commit message or author line.
fail=0
history=$(cd "$OUT" && git log --all -p --format='%an <%ae>%n%cn <%ce>%n%B')
n=0
while IFS= read -r rule || [ -n "$rule" ]; do
  n=$((n + 1))
  [ -z "$rule" ] && continue
  needle=${rule%%==>*}
  needle=${needle#literal:}
  if grep -qF -- "$needle" <<<"$history"; then
    echo "FAIL: rule $n still matches the export"
    fail=1
  fi
done <"$RULES"
if [ -n "${PUBLISH_MAILMAP:-}" ]; then
  # mailmap lines are "New Name <new@email> <old@email>": the old emails must be gone
  while read -r old; do
    grep -qF -- "<$old>" <<<"$history" && { echo "FAIL: an author email the mailmap replaces is still present"; fail=1; }
  done < <(grep -oE '<[^>]+>[[:space:]]*$' "$(expand "$PUBLISH_MAILMAP")" | tr -d '<> \t')
fi
# Independent of the rules: any Tailscale-range address (100.64.0.0/10) is someone's device. The
# range's own network address (100.64.0.0, in firewall docs) is fine.
if leaked=$(grep -oE '\b100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3}\b' <<<"$history" | grep -vx '100\.64\.0\.0'); then
  echo "FAIL: $(wc -l <<<"$leaked" | tr -d ' ') tailnet address(es) in the export; add a rule for each"
  fail=1
fi
(cd "$OUT" && gitleaks detect --source . --redact --no-banner --log-level error) || { echo "FAIL: gitleaks"; fail=1; }
[ "$fail" = 0 ] || { echo "export is NOT clean, nothing pushed: $OUT"; exit 1; }
echo "clean export: $OUT ($(cd "$OUT" && git rev-list --count HEAD) commits, $(cd "$OUT" && git tag | wc -l | tr -d ' ') tags)"

if [ "${1:-}" = "--push" ]; then
  remote=${PUBLIC_REMOTE:?set PUBLIC_REMOTE}
  (cd "$OUT" && git push "$remote" main && git push "$remote" --tags) # never --force
fi
