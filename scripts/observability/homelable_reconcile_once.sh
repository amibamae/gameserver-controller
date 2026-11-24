#!/bin/sh
# Explicitly enabled service only. No provider calls, no deletion.
set -eu
: "${GSC_HOMELABLE_URL:?}"
: "${GSC_HOMELABLE_DESIGN_ID:?}"
: "${GSC_STATE_DB:?}"
: "${GSC_INVENTORY_BIN:?}"
: "${GSC_REPO_DIR:?}"
: "${CREDENTIALS_DIRECTORY:?}"
umask 077
snapshot="$(mktemp)"
trap 'rm -f "$snapshot"' EXIT HUP INT TERM
"$GSC_INVENTORY_BIN" --state-db "$GSC_STATE_DB" \
    --design-id "$GSC_HOMELABLE_DESIGN_ID" > "$snapshot"
python3 "$GSC_REPO_DIR/scripts/observability/homelable_lifecycle.py" \
    --url "$GSC_HOMELABLE_URL" --snapshot "$snapshot" \
    --token-file "$CREDENTIALS_DIRECTORY/token" --apply
