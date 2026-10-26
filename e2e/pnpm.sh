#!/usr/bin/env bash
# End-to-end test for pnpm integration with consona:
# package manager auto-detection, --pm flag override, fresh install,
# lockfile integrity entries for private packages, frozen lockfile reinstall,
# runtime require() execution, pnpm add/remove, and audit privacy filtering.
#
# Needs: go, node, npm, and a credential for the e2e/fixtures.sh repos of
# $OWNER, given explicitly: E2E_GH_CONFIG_DIR / GH_CONFIG_DIR (a gh config
# dir logged in as $OWNER) or CONSONA_TOKEN_<OWNER> in the env.
# Isolated: own port, CONSONA_HOME, CONSONA_CONFIG_DIR (identity, index, logs),
# npmrc, pnpm store, all under $T.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
OWNER=${OWNER:-rakesh-jain-cb1998}
T=${E2E_DIR:-/tmp/consona-pnpm-e2e}
chmod -R u+w "$T" 2>/dev/null || true
rm -rf "$T"; mkdir -p "$T"

# 1. Build consona before changing HOME so Go uses the existing module cache
GOCACHE=$(go env GOCACHE)
GOMODCACHE=$(go env GOMODCACHE)
GOPATH=$(go env GOPATH)
export GOCACHE GOMODCACHE GOPATH

BIN=$T/bin/consona
mkdir -p "$T/bin"
echo "== build consona"
(cd "$ROOT" && go build -o "$BIN" ./cmd/consona) || { echo "build failed"; exit 1; }

OWNER_ENV=CONSONA_TOKEN_$(printf '%s' "$OWNER" | tr 'a-z-' 'A-Z_')
unset GH_TOKEN GITHUB_TOKEN CONSONA_TOKEN    # never a machine-wide token

[ -n "${E2E_GH_CONFIG_DIR:-}" ] && export GH_CONFIG_DIR=$E2E_GH_CONFIG_DIR
if [ -n "${GH_CONFIG_DIR:-}" ]; then
  export GH_CONFIG_DIR=$(cd "$GH_CONFIG_DIR" && pwd)
  LOGIN=$(gh api user --jq .login 2>/dev/null)
  [ "$LOGIN" = "$OWNER" ] || { echo "e2e: the gh login in GH_CONFIG_DIR is '$LOGIN', expected the fixture owner '$OWNER'"; exit 1; }
elif [ -n "${!OWNER_ENV:-}" ]; then
  mkdir -p "$T/gh-empty"; export GH_CONFIG_DIR=$T/gh-empty   # no gh fallback at all
else
  echo "e2e: set E2E_GH_CONFIG_DIR (a gh config dir logged in as $OWNER) or $OWNER_ENV; the default gh login is never used"; exit 1
fi

export CONSONA_PORT=${PNPM_E2E_PORT:-${CONSONA_PORT:-48781}}
export HOME=$T/home
export CONSONA_HOME=$T/consona-home
export CONSONA_CONFIG_DIR=$T/cfg
export npm_config_userconfig=$T/npmrc
export CONSONA_SSH_CONFIG=$T/ssh_config
export CONSONA_SSH_AUTODETECT=0
export npm_config_update_notifier=false
export npm_config_fund=false
export npm_config_audit=false
mkdir -p "$HOME" "$CONSONA_HOME" "$CONSONA_CONFIG_DIR"
: > "$CONSONA_SSH_CONFIG"

FAILS=0
PASSES=0

pass() { printf 'PASS  %s\n' "$*"; PASSES=$((PASSES+1)); }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS+1)); }
check() { local m=$1; shift; if "$@"; then pass "$m"; else fail "$m"; fi; }

show() { sed 's/^/      | /' "$1"; }

cleanup() {
  "$BIN" setup --undo >/dev/null 2>&1 || true
  "$BIN" daemon stop >/dev/null 2>&1 || true
  chmod -R u+w "$T" 2>/dev/null || true
  rm -rf "$T"
}
trap cleanup EXIT

echo "== install pnpm into temp prefix"
PNPM_DIR=$T/pnpm
mkdir -p "$PNPM_DIR"
npm i -g pnpm@latest --prefix "$PNPM_DIR" > "$T/pnpm-install.log" 2>&1 || { echo "pnpm install failed"; cat "$T/pnpm-install.log"; exit 1; }
export PATH=$PNPM_DIR/bin:$PATH
PNPM_VER=$(pnpm --version 2>/dev/null || true)
check "pnpm installed into temp dir ($PNPM_VER)" test -n "$PNPM_VER"

echo "== identity & setup (isolated npmrc, port $CONSONA_PORT)"
"$BIN" init --username e2euser > "$T/init.out" 2>&1; rc=$?
show "$T/init.out"
check "consona init rc=$rc" test $rc -eq 0

"$BIN" setup --no-autostart > "$T/setup.out" 2>&1; rc=$?
show "$T/setup.out"
check "setup rc=$rc and routes @e2euser + @$OWNER" bash -c "[ $rc -eq 0 ] && grep -q '^@e2euser:registry=http://127.0.0.1:$CONSONA_PORT/npm/' '$T/npmrc' && grep -q '^@$OWNER:registry=' '$T/npmrc'"

echo "== register local folder"
mkdir -p "$T/fixtures/local-pkg"
cat > "$T/fixtures/local-pkg/package.json" <<'EOF'
{ "name": "@e2euser/local-pkg", "version": "1.0.0", "main": "index.js" }
EOF
echo 'exports.sayLocal = () => "hello from local-pkg";' > "$T/fixtures/local-pkg/index.js"
"$BIN" register "$T/fixtures/local-pkg" > "$T/reg-local.out" 2>&1; rc=$?
show "$T/reg-local.out"
check "register local folder rc=$rc" bash -c "[ $rc -eq 0 ] && grep -q 'registered @e2euser/local-pkg' '$T/reg-local.out'"

echo "== prepare test application"
mkdir -p "$T/app"
cat > "$T/app/package.json" <<EOF
{
  "name": "pnpm-e2e-app",
  "private": true,
  "version": "1.0.0",
  "dependencies": {
    "@$OWNER/consona-e2e-lib-a": "^1.0.0",
    "@$OWNER/consona-e2e-lib-ts": "^1.0.0",
    "@e2euser/local-pkg": "^1.0.0-0",
    "ms": "^2.1.3"
  },
  "devDependencies": {
    "@types/node": "^20.0.0"
  }
}
EOF

echo "== (1) fresh install with consona install --pm pnpm"
(cd "$T/app" && "$BIN" install --pm pnpm > "$T/inst1.out" 2>&1); rc=$?
show "$T/inst1.out"
check "consona install --pm pnpm rc=$rc" test $rc -eq 0
check "pm message says using pnpm (--pm flag)" grep -q 'consona: using pnpm (--pm flag)' "$T/inst1.out"
check "pnpm install output has consona source summary lines" grep -q '^consona: source:' "$T/inst1.out"
check "sources printed: lib-a, lib-b, lib-ts from github, local-pkg from local" bash -c "grep -q 'source:.*consona-e2e-lib-a' '$T/inst1.out' && grep -q 'source:.*consona-e2e-lib-b' '$T/inst1.out' && grep -q 'source:.*consona-e2e-lib-ts' '$T/inst1.out' && grep -q 'source:.*local-pkg' '$T/inst1.out'"

echo "== check daemon log has registry.redirect for public scoped package (@types/node)"
"$BIN" logs --event registry.redirect --json > "$T/log-redirect.json" 2>&1
show "$T/log-redirect.json"
check "daemon log has registry.redirect event for public scoped package (@types/node)" bash -c "grep -q '\"event\":\"registry.redirect\"' '$T/log-redirect.json' && grep -qE '@types/node|@types%2[fF]node' '$T/log-redirect.json'"

echo "== (2) lockfile integrity entries for private packages"
check "pnpm-lock.yaml exists" test -f "$T/app/pnpm-lock.yaml"

check_lockfile_sha512() {
  local pkg=$1
  awk -v pkg="$pkg" '
    /^packages:/ { in_packages=1; next }
    in_packages && /^[^ ]/ { in_packages=0 }
    in_packages && /^  [^ ]/ && $0 ~ pkg {
      while (getline > 0) {
        if ($0 ~ /resolution:.*integrity: *sha512-/) { ok=1; exit }
        if ($0 ~ /^  [^ ]/ || $0 ~ /^[^ ]/) break
      }
    }
    END { exit(ok ? 0 : 1) }
  ' "$T/app/pnpm-lock.yaml"
}

check "pnpm-lock.yaml has sha512 integrity for lib-a" check_lockfile_sha512 "consona-e2e-lib-a"
check "pnpm-lock.yaml has sha512 integrity for lib-b" check_lockfile_sha512 "consona-e2e-lib-b"
check "pnpm-lock.yaml has sha512 integrity for lib-ts" check_lockfile_sha512 "consona-e2e-lib-ts"
check "pnpm-lock.yaml has sha512 integrity for local-pkg" check_lockfile_sha512 "local-pkg"
check "no lockfile line contains a token-shaped string" bash -c "! grep -qE 'gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}' '$T/app/pnpm-lock.yaml'"

echo "== (3) reinstall with pnpm install --frozen-lockfile after removing node_modules"
rm -rf "$T/app/node_modules"
(cd "$T/app" && pnpm install --frozen-lockfile > "$T/frozen.out" 2>&1); rc=$?
show "$T/frozen.out"
check "pnpm install --frozen-lockfile rc=$rc" test $rc -eq 0

echo "== (4) require() of packages works"
OUT=$(cd "$T/app" && node -e '
const a = require("@'"$OWNER"'/consona-e2e-lib-a");
const ts = require("@'"$OWNER"'/consona-e2e-lib-ts");
const loc = require("@e2euser/local-pkg");
const ms = require("ms");
console.log(a.greet("world"), ts.slugify("Hello World"), loc.sayLocal(), ms(60000));
')
echo "      | output: $OUT"
check "runtime require: lib-a (using transitive lib-b), lib-ts, local-pkg, ms" test "$OUT" = "HELLO WORLD! hello-world hello from local-pkg 1m"
check "transitive lib-b present in node_modules/.pnpm" bash -c "ls -d '$T/app/node_modules/.pnpm/@${OWNER}+consona-e2e-lib-b@'* >/dev/null 2>&1"

echo "== (5) pnpm add / remove of a private package"
(cd "$T/app" && pnpm remove "@$OWNER/consona-e2e-lib-ts" > "$T/remove.out" 2>&1); rc_rem=$?
show "$T/remove.out"
check "pnpm remove private package rc=$rc_rem" bash -c "[ $rc_rem -eq 0 ] && ! grep -q 'consona-e2e-lib-ts' '$T/app/package.json'"

(cd "$T/app" && pnpm add "@$OWNER/consona-e2e-lib-ts" > "$T/add.out" 2>&1); rc_add=$?
show "$T/add.out"
check "pnpm add private package rc=$rc_add" bash -c "[ $rc_add -eq 0 ] && grep -q 'consona-e2e-lib-ts' '$T/app/package.json'"

echo "== (6) auto-detection: pnpm-lock.yaml, package.json packageManager, and overrides"
(cd "$T/app" && "$BIN" install > "$T/autodetect-lock.out" 2>&1); rc_auto=$?
show "$T/autodetect-lock.out"
check "consona install auto-detects pnpm from pnpm-lock.yaml" bash -c "[ $rc_auto -eq 0 ] && grep -q 'consona: using pnpm (pnpm-lock.yaml)' '$T/autodetect-lock.out'"

mkdir -p "$T/detect-app"
cat > "$T/detect-app/package.json" <<'EOF'
{ "name": "detect-app", "private": true, "version": "1.0.0", "packageManager": "pnpm@9.12.0" }
EOF
(cd "$T/detect-app" && "$BIN" install > "$T/autodetect-pm.out" 2>&1); rc_pm=$?
show "$T/autodetect-pm.out"
check "consona install auto-detects pnpm from packageManager field" bash -c "[ $rc_pm -eq 0 ] && grep -q 'consona: using pnpm (package.json packageManager)' '$T/autodetect-pm.out'"

echo "== (6b) --pm npm override in fresh directory"
mkdir -p "$T/override-app"
cp "$T/app/package.json" "$T/app/pnpm-lock.yaml" "$T/override-app/"
(cd "$T/override-app" && "$BIN" install --pm npm > "$T/override-npm.out" 2>&1); rc_over=$?
show "$T/override-npm.out"
check "consona install --pm npm overrides lockfile in fresh dir rc=$rc_over" test $rc_over -eq 0
check "pm message says using npm (--pm flag)" grep -q 'consona: using npm (--pm flag)' "$T/override-npm.out"

echo "== (6c) consona install --quiet (pnpm)"
mkdir -p "$T/quiet-app"
cp "$T/app/package.json" "$T/app/pnpm-lock.yaml" "$T/quiet-app/"
(cd "$T/quiet-app" && "$BIN" install --quiet > "$T/quiet.out" 2>&1); rc_quiet=$?
show "$T/quiet.out"
check "consona install --quiet rc=$rc_quiet" test $rc_quiet -eq 0
check "consona install --quiet prints no 'consona: running:' progress" bash -c "! grep -q 'consona: running:' '$T/quiet.out'"

echo "== (7) pnpm audit filters private names"
(cd "$T/app" && "$BIN" audit > "$T/audit.out" 2>&1); rc_audit=$?
show "$T/audit.out"
check "consona audit rc=$rc_audit" test $rc_audit -eq 0
"$BIN" logs --event audit.filter --json > "$T/log-audit.json" 2>&1
check "audit withheld private packages" bash -c "grep -q 'audit: forwarding.*withheld' '$T/log-audit.json'"

echo "== (8) token leak check"
TOKV=${!OWNER_ENV:-}
[ -n "$TOKV" ] || TOKV=$(gh auth token 2>/dev/null) || TOKV=$(gh config get -h github.com oauth_token 2>/dev/null)
LEAK=0
if [ ${#TOKV} -ge 20 ]; then
  if grep -rqF -- "$TOKV" "$T" 2>/dev/null; then LEAK=1; fi
else
  fail "could not read the fixture token for the leak check"
fi
if grep -rqE 'gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}' "$T"/*.out "$T"/*.json "$CONSONA_CONFIG_DIR" 2>/dev/null; then LEAK=1; fi
unset TOKV
check "no token value in output, log, npmrc or lockfile" test $LEAK -eq 0

echo "== $PASSES passed, $FAILS failed"
exit $FAILS
