#!/usr/bin/env bash
# End-to-end test for local folder sources: init/whoami, register (JS + TS),
# live versions, duplicates, local-wins-over-GitHub, ssh fast paths.
# Needs: go, node/npm and a credential for the e2e/fixtures.sh repos of
# $OWNER, given explicitly: E2E_GH_CONFIG_DIR / GH_CONFIG_DIR (a gh config
# dir logged in as $OWNER; checked) or CONSONA_TOKEN_<OWNER> in the env.
# The machine's default gh login, GH_TOKEN/GITHUB_TOKEN/CONSONA_TOKEN and
# named tokens are never used. Isolated: own port, CONSONA_HOME,
# CONSONA_CONFIG_DIR (identity, index, logs), npmrc and an empty ssh config,
# all under $T. Runs on Linux and macOS.
# KEEP_REMOTE=1 leaves the registered folders and $T/remote-serve in place,
# so another machine can run e2e/ssh-client.sh against this one.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OWNER=${OWNER:-rakesh-jain-cb1998}
T=${E2E_DIR:-/tmp/consona-local-e2e}
rm -rf "$T"; mkdir -p "$T"
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
export CONSONA_PORT=${CONSONA_PORT:-48761} CONSONA_HOME=$T/home CONSONA_CONFIG_DIR=$T/cfg npm_config_userconfig=$T/npmrc
export CONSONA_SSH_CONFIG=$T/ssh_config npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
: > "$CONSONA_SSH_CONFIG"
BIN=$T/bin/consona
FAILS=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS+1)); }
check() { local m=$1; shift; if "$@"; then pass "$m"; else fail "$m"; fi; }
# Portable helpers (GNU/Linux and macOS/BSD).
if command -v perl >/dev/null 2>&1; then now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }
else now() { python3 -c 'import time; print("%.3f" % time.time())'; }; fi
perm() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
if command -v sha256sum >/dev/null 2>&1; then sha256() { sha256sum "$@"; } else sha256() { shasum -a 256 "$@"; }; fi
export -f perm sha256
secs() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", b-a}'; }
show() { sed 's/^/      | /' "$1"; }
FIX=$T/fixtures
mkdir -p "$FIX/js-lib" "$FIX/ts-lib/src" "$FIX/shadow-lib-a"
echo 'exports.greet = (n) => `hello ${n} from js-lib v1`;' > "$FIX/js-lib/index.js"
echo 'not published (outside files)' > "$FIX/js-lib/notes.txt"
echo '{ "name": "js-lib", "version": "1.0.0", "main": "index.js", "files": ["index.js"] }' > "$FIX/js-lib/package.json"
cat > "$FIX/ts-lib/package.json" <<'J'
{ "name": "@e2euser/ts-lib", "version": "0.1.0", "main": "dist/index.js", "types": "dist/index.d.ts",
  "files": ["dist"], "scripts": { "prepare": "tsc -p ." }, "devDependencies": { "typescript": "^5.5.4" } }
J
echo 'export const shout = (s: string): string => s.toUpperCase() + "!";' > "$FIX/ts-lib/src/index.ts"
echo '{ "compilerOptions": { "outDir": "dist", "declaration": true, "module": "commonjs", "target": "es2019", "strict": true }, "include": ["src"] }' > "$FIX/ts-lib/tsconfig.json"
echo 'exports.greet = (n) => `LOCAL shadow of lib-a says hi to ${n}`;' > "$FIX/shadow-lib-a/index.js"
echo "{ \"name\": \"@$OWNER/consona-e2e-lib-a\", \"version\": \"1.0.0\", \"main\": \"index.js\" }" > "$FIX/shadow-lib-a/package.json"
JS_ORIG=$(cat "$FIX/js-lib/index.js")
cleanup() {
  printf '%s\n' "$JS_ORIG" > "$FIX/js-lib/index.js"
  if [ -z "${KEEP_REMOTE:-}" ]; then
    "$BIN" setup --undo >/dev/null 2>&1; "$BIN" daemon stop >/dev/null 2>&1; rm -rf "$T"
  else
    "$BIN" daemon stop >/dev/null 2>&1
  fi
}
trap cleanup EXIT
(cd "$ROOT" && go build -o "$BIN" ./cmd/consona) || { echo build failed; exit 1; }
FIXHASH_BEFORE=$(cd "$FIX" && find . -type f | sort | xargs bash -c 'sha256 "$@"' _ | sha256)

echo "== identity"
"$BIN" register "$FIX/js-lib" > "$T/noinit.out" 2>&1; rc=$?
check "register before init is refused (rc=$rc)" bash -c "[ $rc -ne 0 ] && grep -q 'consona init' '$T/noinit.out'"
"$BIN" init > "$T/init0.out" 2>&1 </dev/null; rc=$?
check "init without --username and no terminal fails clearly" bash -c "[ $rc -ne 0 ] && grep -q -- '--username' '$T/init0.out'"
"$BIN" init --username E2EUser > "$T/init.out" 2>&1; show "$T/init.out"
"$BIN" whoami > "$T/whoami.out"; show "$T/whoami.out"
check "identity: username normalised, device id, files 0700/0600" bash -c "grep -q '@e2euser' '$T/whoami.out' && [ \$(perm '$CONSONA_CONFIG_DIR') = 700 ] && [ \$(perm '$CONSONA_CONFIG_DIR/identity.json') = 600 ]"
"$BIN" init --username other > "$T/reinit.out" 2>&1; rc=$?
check "re-init with another username needs --force" test $rc -ne 0

echo "== setup (isolated npmrc, port $CONSONA_PORT)"
"$BIN" setup --no-autostart > "$T/setup.out" 2>&1; rc=$?; show "$T/setup.out"
check "setup rc=0 and routes @e2euser + @$OWNER" bash -c "[ $rc -eq 0 ] && grep -q '^@e2euser:registry=http://127.0.0.1:$CONSONA_PORT/npm/' '$T/npmrc' && grep -q '^@$OWNER:registry=' '$T/npmrc'"

echo "== (a) register JS + TS folders, install, change, reinstall"
"$BIN" register "$FIX/js-lib" > "$T/reg1.out" 2>&1; rc=$?; show "$T/reg1.out"
check "register js-lib (default name forced into @e2euser)" bash -c "[ $rc -eq 0 ] && grep -q 'registered @e2euser/js-lib' '$T/reg1.out'"
"$BIN" register "$FIX/ts-lib" > "$T/reg2.out" 2>&1; rc=$?; show "$T/reg2.out"
check "register ts-lib" test $rc -eq 0
"$BIN" registered > "$T/registered.out"; show "$T/registered.out"
mkdir -p "$T/app"; echo '{"name":"lssh-app","private":true,"version":"0.0.0"}' > "$T/app/package.json"
t0=$(now); (cd "$T/app" && "$BIN" install @e2euser/js-lib @e2euser/ts-lib > "$T/inst1.out" 2>&1); rc=$?; t1=$(now)
show "$T/inst1.out"
check "install both local packages rc=$rc [$(secs $t0 $t1)s cold, includes tsc build]" test $rc -eq 0
check "sources printed: both <- local" bash -c "grep -q 'source: @e2euser/js-lib@1.0.0-local.1.h.* <- local $FIX/js-lib' '$T/inst1.out' && grep -q 'source: @e2euser/ts-lib@0.1.0-local.1.h.* <- local' '$T/inst1.out'"
OUT=$(cd "$T/app" && node -e 'console.log(require("@e2euser/js-lib").greet("tirth"), require("@e2euser/ts-lib").shout("ts"))')
check "packages work: $OUT" test "$OUT" = "hello tirth from js-lib v1 TS!"
check "files/.npmignore respected (notes.txt not packed; ts dist + d.ts packed, src not)" bash -c "[ ! -e '$T/app/node_modules/@e2euser/js-lib/notes.txt' ] && [ -f '$T/app/node_modules/@e2euser/ts-lib/dist/index.d.ts' ] && [ ! -e '$T/app/node_modules/@e2euser/ts-lib/src' ]"
PN=$(node -p 'require(process.argv[1]).name' "$T/app/node_modules/@e2euser/js-lib/package.json"); FN=$(node -p 'require(process.argv[1]).name' "$FIX/js-lib/package.json")
check "packed package.json renamed to the registered name ($PN), folder untouched ($FN)" bash -c "[ '$PN' = @e2euser/js-lib ] && [ '$FN' = js-lib ]"
V1=$(node -p 'require(process.argv[1]).packages["node_modules/@e2euser/js-lib"].integrity' "$T/app/package-lock.json")
rm -rf "$T/app/node_modules"
t0=$(now); (cd "$T/app" && "$BIN" install > "$T/inst2.out" 2>&1); rc=$?; t1=$(now)
WARM=$(secs $t0 $t1)
check "unchanged folder: reinstall from lockfile rc=$rc, same integrity [${WARM}s warm]" bash -c "[ $rc -eq 0 ] && [ \"\$(node -p 'require(\"$T/app/package-lock.json\").packages[\"node_modules/@e2euser/js-lib\"].integrity')\" = '$V1' ]"
printf '%s\n' 'exports.greet = (n) => `hello ${n} from js-lib v2 (edited)`;' > "$FIX/js-lib/index.js"
t0=$(now); (cd "$T/app" && "$BIN" install > "$T/inst3.out" 2>&1); rc=$?; t1=$(now); show "$T/inst3.out"
OUT=$(cd "$T/app" && node -e 'console.log(require("@e2euser/js-lib").greet("tirth"))')
V2=$(node -p 'require(process.argv[1]).packages["node_modules/@e2euser/js-lib"].version' "$T/app/package-lock.json")
I2=$(node -p 'require(process.argv[1]).packages["node_modules/@e2euser/js-lib"].integrity' "$T/app/package-lock.json")
check "edited folder: plain 'consona install' picks it up rc=$rc [$(secs $t0 $t1)s]: $OUT ($V2)" bash -c "[ $rc -eq 0 ] && [ '$OUT' = 'hello tirth from js-lib v2 (edited)' ] && [[ '$V2' == 1.0.0-local.2.h* ]] && [ '$I2' != '$V1' ]"
check "ts-lib (unchanged) kept its version" bash -c "node -p 'require(\"$T/app/package-lock.json\").packages[\"node_modules/@e2euser/ts-lib\"].version' | grep -q '^0.1.0-local.1.h'"
FIXHASH_AFTER_EDIT=$(cd "$FIX" && find . -type f -not -path './js-lib/index.js' | sort | xargs bash -c 'sha256 "$@"' _ | sha256)
check "registered folders never modified by consona (no node_modules/lockfile/dist written)" bash -c "[ ! -e '$FIX/ts-lib/node_modules' ] && [ ! -e '$FIX/ts-lib/dist' ] && [ ! -e '$FIX/js-lib/package-lock.json' ] && [ ! -e '$FIX/ts-lib/package-lock.json' ]"

echo "== (b) duplicates"
"$BIN" register "$FIX/js-lib" --name @e2euser/js-lib-again > "$T/dup1.out" 2>&1; rc1=$?; show "$T/dup1.out"
mkdir -p "$T/other"; cp "$FIX/js-lib/package.json" "$T/other/"
"$BIN" register "$T/other" > "$T/dup2.out" 2>&1; rc2=$?; show "$T/dup2.out"
ln -sfn "$FIX/ts-lib" "$T/ts-link"
"$BIN" register "$T/ts-link" --name @e2euser/ts-lib-via-link > "$T/dup3.out" 2>&1; rc3=$?; show "$T/dup3.out"
"$BIN" register "$FIX/js-lib" --name @types/node > "$T/dup4.out" 2>&1; rc4=$?; show "$T/dup4.out"
check "same folder under a 2nd name, same name from a 2nd folder, same folder via symlink, foreign public scope: all refused" bash -c "[ $rc1 -ne 0 ] && [ $rc2 -ne 0 ] && [ $rc3 -ne 0 ] && [ $rc4 -ne 0 ] && grep -q -- --replace '$T/dup1.out'"
check "index still has exactly 2 entries" test "$(node -p 'require(process.argv[1]).entries.length' "$CONSONA_CONFIG_DIR/local-index.json")" = 2

echo "== (d) local wins over GitHub (same name as a $OWNER fixture)"
"$BIN" which "@$OWNER/consona-e2e-lib-a" > "$T/which0.out" 2>&1; show "$T/which0.out"
"$BIN" register "$FIX/shadow-lib-a" > "$T/shadow-default.out" 2>&1; show "$T/shadow-default.out"
check "a folder whose package.json is in another scope defaults to @e2euser" grep -q 'registered @e2euser/consona-e2e-lib-a' "$T/shadow-default.out"
"$BIN" register "$FIX/shadow-lib-a" --name "@$OWNER/consona-e2e-lib-a" --replace > "$T/shadow.out" 2>&1; rc=$?; show "$T/shadow.out"
check "register shadow of @$OWNER/consona-e2e-lib-a (notes the GitHub repo)" bash -c "[ $rc -eq 0 ] && grep -q 'also exists on GitHub' '$T/shadow.out'"
"$BIN" which "@$OWNER/consona-e2e-lib-a" > "$T/which1.out" 2>&1; show "$T/which1.out"
mkdir -p "$T/app2"; echo '{"name":"lssh-app2","private":true,"version":"0.0.0"}' > "$T/app2/package.json"
(cd "$T/app2" && "$BIN" install "@$OWNER/consona-e2e-lib-a" "@$OWNER/consona-e2e-lib-c" > "$T/inst4.out" 2>&1); rc=$?; show "$T/inst4.out"
OUT=$(cd "$T/app2" && node -e "console.log(require('@$OWNER/consona-e2e-lib-a').greet('tirth'))")
check "local wins: lib-a from the folder ($OUT), lib-c still from GitHub" bash -c "[ $rc -eq 0 ] && grep -q 'consona-e2e-lib-a@1.0.0-local.1.h.* <- local' '$T/inst4.out' && grep -q 'consona-e2e-lib-c@.* <- github github.com/$OWNER/consona-e2e-lib-c' '$T/inst4.out' && [[ '$OUT' == LOCAL* ]]"
"$BIN" unregister "@$OWNER/consona-e2e-lib-a" > "$T/unreg.out" 2>&1; show "$T/unreg.out"
"$BIN" which "@$OWNER/consona-e2e-lib-a" > "$T/which2.out" 2>&1; show "$T/which2.out"
check "after unregister the GitHub repo serves it again" grep -q '<- github' "$T/which2.out"

echo "== event log (logstore in \$CONSONA_CONFIG_DIR/logs)"
"$BIN" logs --event source.resolve --json > "$T/log-resolve.json" 2>&1
"$BIN" logs --event local --json > "$T/log-local.json" 2>&1
grep -h '"source":"' "$T/log-resolve.json" | sed 's/.*"package":"\([^"]*\)".*"source":"\([a-z]*\)".*/      | source.resolve \1 <- \2/' | sort -u
check "logs record local and github resolutions (source.resolve, fields.source)" bash -c "grep -q '\"source\":\"local\"' '$T/log-resolve.json' && grep -q '\"source\":\"github\".*' '$T/log-resolve.json' && grep -q '\"package\":\"@e2euser/js-lib\"' '$T/log-resolve.json'"
check "logs record register and unregister" bash -c "grep -q '\"event\":\"local.register\"' '$T/log-local.json' && grep -q '\"event\":\"local.unregister\"' '$T/log-local.json'"
check "log store lives in the config dir, 0700, files 0600" bash -c "[ -d '$CONSONA_CONFIG_DIR/logs' ] && [ \$(perm '$CONSONA_CONFIG_DIR/logs') = 700 ] && for f in '$CONSONA_CONFIG_DIR'/logs/*; do [ -d \"\$f\" ] || [ \$(perm \"\$f\") = 600 ] || exit 1; done"
TOKV=${!OWNER_ENV:-$(gh auth token 2>/dev/null)}   # compared only, never printed
LEAK=0
if [ -n "$TOKV" ]; then grep -rqF -- "$TOKV" "$CONSONA_CONFIG_DIR" "$CONSONA_HOME" "$T"/*.out "$T"/*.json 2>/dev/null && LEAK=1; fi
grep -rqE 'gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}' "$T/log-resolve.json" "$T/log-local.json" "$CONSONA_CONFIG_DIR/logs" 2>/dev/null && LEAK=1
unset TOKV
check "no token value (or token-shaped string) in logs, config, consona home or outputs" test $LEAK -eq 0

echo "== perf: names that resolve elsewhere never wait for ssh"
printf 'Host dead-host\n  HostName 192.0.2.1\n' > "$CONSONA_SSH_CONFIG"
"$BIN" daemon stop >/dev/null 2>&1
t0=$(now); "$BIN" which "@$OWNER/consona-e2e-lib-c" > "$T/which3.out" 2>&1; t1=$(now); show "$T/which3.out"
P1=$(secs $t0 $t1)
t0=$(now); "$BIN" which "@$OWNER/consona-e2e-lib-c" > /dev/null 2>&1; t1=$(now); P2=$(secs $t0 $t1)
t0=$(now); "$BIN" which "@e2euser/js-lib" > /dev/null 2>&1; t1=$(now); P3=$(secs $t0 $t1)
echo "      | GitHub name with an unreachable ssh-config host: first ${P1}s (one ConnectTimeout, in parallel with GitHub), then ${P2}s (host remembered as down); local name ${P3}s (no ssh)"
check "unreachable auto-detected host costs <=~3.5s once, then nothing" bash -c "awk 'BEGIN{exit !($P1 < 4.5 && $P2 < 2.0 && $P3 < 1.0)}'"
: > "$CONSONA_SSH_CONFIG"; rm -f "$CONSONA_HOME/remotes-state.json"

if [ -n "${KEEP_REMOTE:-}" ]; then
  # Remote side for e2e/ssh-client.sh: same registered folders, a separate home.
  cat > "$T/remote-serve" <<W
#!/bin/sh
# GitHub deps of builds here use this machine's own credential chain
# (the fixture owner's GH_CONFIG_DIR); nothing is forwarded from the client.
unset GH_TOKEN GITHUB_TOKEN CONSONA_TOKEN
export CONSONA_CONFIG_DIR='$CONSONA_CONFIG_DIR' CONSONA_HOME='$T/remote-home' npm_config_userconfig='$T/remote-npmrc' PATH='$PATH'
export GH_CONFIG_DIR='$GH_CONFIG_DIR' CONSONA_SSH_CONFIG='$CONSONA_SSH_CONFIG' CONSONA_SSH_AUTODETECT=0
exec '$BIN' "\$@"
W
  chmod 755 "$T/remote-serve"; : > "$T/remote-npmrc"
  "$BIN" setup --undo > /dev/null 2>&1
fi
FIXHASH_FINAL=$( printf '%s\n' "$JS_ORIG" > "$FIX/js-lib/index.js"; cd "$FIX" && find . -type f | sort | xargs bash -c 'sha256 "$@"' _ | sha256)
check "fixtures restored byte-for-byte" test "$FIXHASH_FINAL" = "$FIXHASH_BEFORE"
echo "== $FAILS failure(s)"
exit $FAILS
