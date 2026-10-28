#!/usr/bin/env bash
# End-to-end check for `consona snapshot` with real npm and pnpm against the
# rakesh-jain-cb1998/consona-test-fixtures-pins fixture (read only: nothing in
# the fixture is changed). Needs go, node, npm, pnpm and a token that can read
# the fixture, given as CONSONA_TOKEN_RAKESH_JAIN_CB1998 in the environment.
# Isolated: own port, home, config, npmrc and project dirs under $T.
# Not covered: a live push during an install (the frozen-state check below
# edits a copy of the snapshot instead), Windows, macOS, CI.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
: "${CONSONA_TOKEN_RAKESH_JAIN_CB1998:?set CONSONA_TOKEN_RAKESH_JAIN_CB1998 to a token that can read the fixture}"
T=${E2E_DIR:-/tmp/consona-snapshot-e2e}; chmod -R u+w "$T" 2>/dev/null; rm -rf "$T"
mkdir -p "$T"/{bin,home,ch,cfg,gh-empty,proj,pn,proj2}
(cd "$ROOT" && go build -o "$T/bin/consona" ./cmd/consona) || { echo "build failed"; exit 1; }
BIN=$T/bin/consona
unset GH_TOKEN GITHUB_TOKEN CONSONA_TOKEN
export GH_CONFIG_DIR=$T/gh-empty CONSONA_PORT=${SNAP_E2E_PORT:-48777} HOME=$T/home CONSONA_HOME=$T/ch \
  CONSONA_CONFIG_DIR=$T/cfg npm_config_userconfig=$T/npmrc CONSONA_SSH_CONFIG=$T/ssh CONSONA_SSH_AUTODETECT=0 \
  npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
: > "$T/ssh"
R=rakesh-jain-cb1998/consona-test-fixtures-pins
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "ok   $1"; }
bad() { FAIL=$((FAIL+1)); echo "FAIL $1"; }
check() { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }
cleanup() { "$BIN" daemon stop >/dev/null 2>&1; }; trap cleanup EXIT

"$BIN" setup --no-autostart >/dev/null 2>&1; "$BIN" map sync $R >/dev/null 2>&1; "$BIN" daemon start >/dev/null 2>&1
cd "$T/proj"; echo '{"name":"app","version":"1.0.0","dependencies":{"@consona-fixtures/pin-ui":"^1.0.0"}}' > package.json
check "snapshot create"      '"$BIN" snapshot create'
check "snapshot use"         '"$BIN" snapshot use && grep -q "/npm/s/" .npmrc'
check "npm install"          'npm install && node -e "process.exit(require(\"@consona-fixtures/pin-ui\")()===\"pin-core v1 + pin-ui\"?0:1)"'
check "lockfile URLs carry no snapshot path" '! grep -q "/npm/s/" package-lock.json'
check "npm ci"               'rm -rf node_modules && npm ci'
cp package.json .npmrc consona.snapshot.json "$T/pn/"; cd "$T/pn"
check "pnpm install"         'pnpm install && node -e "process.exit(require(\"@consona-fixtures/pin-ui\")()===\"pin-core v1 + pin-ui\"?0:1)"'
cd "$T/proj2"; cp ../proj/package.json .
python3 - <<'EOF'
import json
s=json.load(open('../proj/consona.snapshot.json'))
for r in s['repos'].values(): r['tags']={}
json.dump(s,open('consona.snapshot.json','w'))
EOF
"$BIN" snapshot use >/dev/null 2>&1
check "frozen state wins over live (no release tag recorded => ETARGET)" '[[ "$(npm install 2>&1)" == *ETARGET* ]]'
cd "$T/proj"; rm -rf node_modules package-lock.json
"$BIN" map remove @consona-fixtures/pin-core >/dev/null 2>&1
check "changed mapping fails closed with 409" '[[ "$(npm install 2>&1)" == *E409* ]]'
check "snapshot unuse"       '"$BIN" snapshot unuse && ! grep -q "consona snapshot" .npmrc'
echo "passed $PASS, failed $FAIL"; [ "$FAIL" = 0 ]
