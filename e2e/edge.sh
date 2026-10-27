#!/usr/bin/env bash
# Edge-case end-to-end tests against the private consona-fx-* fixture repos
# (see docs/edge-cases.md for the fixture list and what each case expects).
# Lanes run in parallel, each with its own daemon port, HOME, consona
# home/config dir, npmrc and npm cache:
#   resolve  semver ranges, prerelease, junk tags, no-v tags, develop-only
#            default branch, name mismatch
#   graph    deep private chain, dependency cycle (+ daemon CPU after it), peer deps
#   pkg      bin (.bin + npx), ESM-only, monorepo root, files/.npmignore tarball
#            contents, large files, prepare+prepack, build failure
#   repo     archived repo, renamed repo via the old name, tag force-moved
#            (fresh resolve, lockfile pinned to the old commit)
#   lock     consona install of a mixed app, lockfile addresses, reinstall from
#            the lockfile (warm, then with empty consona and npm caches), npm audit
#   audit    audit through a local capture "upstream": no private name leaves
# Every lane: logs show each resolution, only the fixture credential is used,
# gh is never run.
#
# Credential: ONLY CONSONA_TOKEN_<OWNER> in this process's environment (never an
# argument, never written). The daemons are started with --no-autostart from
# this process, GH_CONFIG_DIR is an empty dir and `gh` on PATH is a tripwire
# that records any call, so no gh / Keychain login of the machine can be used.
# Zero public npm packages are installed. Nothing outside the temp dir is
# touched; no Keychain or secure-store operation is made.
#
#   CONSONA_TOKEN_RAKESH_JAIN_CB1998=... e2e/edge.sh [lane ...]
# Env: OWNER (rakesh-jain-cb1998), EDGE_PORT_BASE (48760; lane i uses base+i,
# the capture upstream base+9), EDGE_OUT (copy results/logs there), KEEP=1.
set -uo pipefail
set +x   # never trace: the token is in the environment
OWNER=${OWNER:-rakesh-jain-cb1998}
O=@$OWNER
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OWNER_ENV=CONSONA_TOKEN_$(printf '%s' "$OWNER" | tr 'a-z-' 'A-Z_')
TOK=${!OWNER_ENV:-}
[ ${#TOK} -ge 20 ] || { echo "edge: set $OWNER_ENV (the fixture owner's token) in this process's environment"; exit 2; }
export "$OWNER_ENV"
# No other credential may reach a daemon.
unset GH_TOKEN GITHUB_TOKEN CONSONA_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST
for v in $(compgen -e); do case $v in CONSONA_TOKEN_*) [ "$v" = "$OWNER_ENV" ] || unset "$v";; esac; done
BASE=${EDGE_PORT_BASE:-48760}
for p in 48730 48731; do [ "$p" -ge "$BASE" ] && [ "$p" -le $((BASE+9)) ] && { echo "edge: port range $BASE-$((BASE+9)) overlaps $p"; exit 2; }; done
OS=$(uname -s)
LANES=${*:-resolve graph pkg repo lock audit}
TMP=$(mktemp -d "${TMPDIR:-/tmp}/consona-edge.XXXXXX")
TMP=$(cd "$TMP" && pwd -P)
BIN=$TMP/bin/consona

if command -v perl >/dev/null 2>&1; then now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }
else now() { python3 -c 'import time; print("%.3f" % time.time())'; }; fi
secs() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", b-a}'; }
tmo() { local s=$1; shift
  if command -v timeout >/dev/null 2>&1; then timeout "$s" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then gtimeout "$s" "$@"
  else perl -e 'alarm shift; exec @ARGV or die "exec: $!"' "$s" "$@"; fi; }
sha512b64() { node -e 'process.stdout.write(require("crypto").createHash("sha512").update(require("fs").readFileSync(process.argv[1])).digest("base64"))' "$1"; }
fsize() { wc -c < "$1" | tr -d ' '; }
# GitHub API with the fixture token, which goes to curl on stdin (-K -), never in argv.
ghapi() { printf 'header = "Authorization: Bearer %s"\nheader = "Accept: application/vnd.github+json"\n' "$TOK" | curl -fsSL -K - "https://api.github.com$1"; }

T0=$(now)
LOGIN=$(ghapi /user | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{console.log(JSON.parse(s).login)}catch(e){}})')
[ "$LOGIN" = "$OWNER" ] || { echo "edge: $OWNER_ENV belongs to '$LOGIN', expected $OWNER"; rm -rf "$TMP"; exit 2; }
(cd "$ROOT" && go build -o "$BIN" ./cmd/consona) || { echo "edge: build failed"; rm -rf "$TMP"; exit 2; }
mkdir -p "$TMP/shim" "$TMP/gh-empty"; : > "$TMP/gh-calls.log"
cat > "$TMP/shim/gh" <<SH
#!/bin/sh
echo "gh called: \$*" >> "$TMP/gh-calls.log"
exit 1
SH
chmod +x "$TMP/shim/gh"
export PATH=$TMP/shim:$PATH

cleanup() {
  for d in "$TMP"/lane-*; do
    [ -f "$d/port" ] || continue
    (export CONSONA_PORT=$(cat "$d/port") CONSONA_HOME=$d/consona-home CONSONA_CONFIG_DIR=$d/cfg HOME=$d/home npm_config_userconfig=$d/npmrc
     "$BIN" setup --undo >/dev/null 2>&1; "$BIN" daemon stop >/dev/null 2>&1)
  done
  [ -f "$TMP/capture.pid" ] && kill "$(cat "$TMP/capture.pid")" 2>/dev/null
  [ -z "${KEEP:-}" ] && rm -rf "$TMP"
}
trap cleanup EXIT

# ---------- per-lane helpers ----------
res() {   # res CASE STATUS EXPECTED ACTUAL
  printf '%s\t%s\t%s\t%s\t%s\n' "$LANE" "$1" "$2" "$3" "$4" | tr -d '\r' >> "$RES"
  printf '%-5s %-8s %-34s %s\n' "$2" "$LANE" "$1" "$4"
}
expect() { if [ "$2" = "$3" ]; then res "$1" PASS "$2" "$3"; else res "$1" FAIL "$2" "$3"; fi; }
ok() { local c=$1 e=$2; shift 2; if "$@"; then res "$c" PASS "$e" "as expected"; else res "$c" FAIL "$e" "check failed: $*"; fi; }
one() { tr '\n' ' ' < "$1" | sed 's/  */ /g' | cut -c1-"${2:-300}"; }
lane_init() {   # lane_init NAME PORT_OFFSET [UPSTREAM]
  LANE=$1 PORT=$((BASE+$2)) L=$TMP/lane-$1
  RES=$L/results.tsv
  mkdir -p "$L/home" "$L/tmp" "$L/cfg" "$L/apps"; : > "$RES"; : > "$L/ssh_config"; echo "$PORT" > "$L/port"
  printf 'registry=%s\n' "${3:-https://registry.npmjs.org/}" > "$L/npmrc"
  for v in $(compgen -e); do case $v in PATH|USER|LOGNAME|LANG|LC_ALL|LC_CTYPE|TERM|SHELL|"$OWNER_ENV") ;; *) unset "$v" 2>/dev/null;; esac; done
  export HOME=$L/home TMPDIR=$L/tmp CONSONA_PORT=$PORT CONSONA_HOME=$L/consona-home CONSONA_CONFIG_DIR=$L/cfg \
    CONSONA_SSH_CONFIG=$L/ssh_config CONSONA_SSH_AUTODETECT=0 GH_CONFIG_DIR=$TMP/gh-empty \
    XDG_CONFIG_HOME=$L/home/.config XDG_CACHE_HOME=$L/home/.cache \
    npm_config_userconfig=$L/npmrc npm_config_cache=$L/npm-cache npm_config_update_notifier=false npm_config_fund=false
  LT0=$(now)
  "$BIN" setup --no-autostart > "$L/setup.out" 2>&1; local rc=$?
  ok setup "setup --no-autostart routes $O, daemon on 127.0.0.1:$PORT" \
    bash -c "[ $rc -eq 0 ] && grep -q '$O' '$L/setup.out' && curl -sf http://127.0.0.1:$PORT/-/consona/ping >/dev/null"
}
app() {   # app NAME [DEPS_JSON] -> creates $L/apps/NAME, cd's there
  mkdir -p "$L/apps/$1"; cd "$L/apps/$1" || exit 1
  local deps=${2:-}; [ -n "$deps" ] || deps='{}'
  printf '{ "name": "edge-%s", "version": "0.0.0", "private": true, "dependencies": %s }\n' "$1" "$deps" > package.json
}
npmi() {   # npmi OUTNAME args... : npm in the current app, timed, output in $L/OUTNAME.out
  local name=$1 out=$L/$1.out; shift; local t=$(now)
  tmo 300 npm "$@" > "$out" 2>&1; local rc=$?
  echo "npm $* [$name] $(secs "$t" "$(now)")s rc=$rc" >> "$L/timings.txt"; return $rc
}
ver() { node -p "require('./node_modules/$1/package.json').version" 2>/dev/null || echo none; }
pjname() { node -p "require('./node_modules/$1/package.json').name" 2>/dev/null || echo none; }
hello() { node -e "console.log(require('$1').hello())" 2>&1 | head -1; }
tarlist() {   # tarlist PKG -> sorted file list of the tarball consona serves for the installed version
  local url f=$L/tmp/$(echo "$1" | tr '@/' '__').tgz
  url=$(npm view "$1@$(ver "$1")" dist.tarball 2>/dev/null)
  curl -sf -o "$f" "$url" && tar tzf "$f" | sort | tr '\n' ' ' | sed 's/ $//'
  echo "$f" > "$L/tmp/last-tgz"
}
dpid() { if command -v ss >/dev/null 2>&1; then ss -ltnp 2>/dev/null | grep "127.0.0.1:$PORT " | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2
  else lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1; fi; }
cpusecs() {
  if [ -r "/proc/$1/stat" ]; then awk -v t="$(getconf CLK_TCK)" '{printf "%.2f\n", ($14+$15)/t}' "/proc/$1/stat"; return; fi
  ps -o time= -p "$1" 2>/dev/null | awk -F: '{s=0; for(i=1;i<=NF;i++){ if ($i ~ /-/) { split($i,a,"-"); s=s*60+a[1]*86400+a[2] } else s=s*60+$i } printf "%.2f\n", s}'; }
cpu_window() { local p; p=$(dpid); [ -n "$p" ] || { echo "nopid"; return; }
  local a b; a=$(cpusecs "$p"); sleep "$1"; b=$(cpusecs "$p"); awk -v a="$a" -v b="$b" 'BEGIN{printf "%.2f", b-a}'; }
lane_finish() {   # logs + credential checks common to all lanes; $1 = packages that must have a source.resolve
  cd "$L" || return
  "$BIN" logs export --format jsonl -o "$L/logs.jsonl" > /dev/null 2>&1
  "$BIN" logs --event source --limit 500 > "$L/logs-source.txt" 2>&1
  "$BIN" logs --errors --limit 200 > "$L/logs-errors.txt" 2>&1
  local missing
  missing=$(node -e '
    const ls=require("fs").readFileSync(process.argv[1],"utf8").split("\n").filter(Boolean).map(JSON.parse);
    const got=new Set(ls.filter(e=>e.event==="source.resolve").map(e=>e.package));
    console.log(process.argv.slice(3).filter(p=>!got.has(process.argv[2]+"/"+p)).map(p=>" "+p).join(""))' "$L/logs.jsonl" "$O" $1)
  expect "logs: source.resolve per package" "all of: $1" "$( [ -z "$missing" ] && echo "all of: $1" || echo "missing:$missing")"
  ok "logs: consona logs (human) lists them" "every package in 'consona logs --event source'" \
    bash -c "for p in $1; do grep -q '$O/'\$p '$L/logs-source.txt' || exit 1; done"
  local srcs; srcs=$(node -e '
    const ls=require("fs").readFileSync(process.argv[1],"utf8").split("\n").filter(Boolean).map(JSON.parse);
    const s=new Set(ls.filter(e=>e.event&&e.event.startsWith("credential")).map(e=>e.credential_source||"?"));
    console.log([...s].sort().join(", ")||"none")' "$L/logs.jsonl")
  expect "credentials: only $OWNER_ENV used" "env $OWNER_ENV" "$srcs"
  ok "credentials: daemon log has no gh login" "no 'gh login' credential in daemon.log / logs" \
    bash -c "! grep -qi 'using gh login\|\"credential_source\":\"gh' '$L/logs.jsonl' $(ls "$CONSONA_HOME"/daemon.log 2>/dev/null)"
  "$BIN" setup --undo > "$L/undo.out" 2>&1
  ok "undo" "setup --undo stops the daemon" bash -c "! curl -sf http://127.0.0.1:$PORT/-/consona/ping >/dev/null"
  cp "$CONSONA_HOME/daemon.log" "$L/daemon.log" 2>/dev/null
  [ -f "$L/consona-home.warm/daemon.log" ] && cp "$L/consona-home.warm/daemon.log" "$L/daemon-warm.log"
  echo "lane $(secs "$LT0" "$(now)")s" >> "$L/timings.txt"
}

# ---------- lanes ----------
lane_resolve() {
  lane_init resolve 0
  local P=$O/consona-fx-many-tags
  app view; npm view "$P" versions --json > "$L/mt-versions.out" 2> "$L/mt-versions.err"  # npm logs (consona sets loglevel) stay out of the JSON
  expect "many-tags: versions listed" '["1.0.0","1.1.0","1.2.3","2.0.0","2.1.0-beta.1"]' "$(node -p 'JSON.stringify(JSON.parse(require("fs").readFileSync(process.argv[1])))' "$L/mt-versions.out" 2>/dev/null)"
  expect "many-tags: dist-tags" '{"latest":"2.0.0"}' "$(npm view "$P" dist-tags --json 2>/dev/null | node -p 'JSON.stringify(JSON.parse(require("fs").readFileSync(0)))')"
  local spec want i=0
  for pair in "^1:1.2.3" "^1.0.0:1.2.3" "~1.1.0:1.1.0" "1.1.0:1.1.0" "^2:2.0.0" "^2.0.0:2.0.0" "2.1.0-beta.1:2.1.0-beta.1" "*:2.0.0" "latest:2.0.0" ">=2.1.0-beta.0:2.1.0-beta.1"; do
    spec=${pair%:*} want=${pair##*:}; i=$((i+1))
    app "mt$i" "{\"$P\": \"$spec\"}"; npmi "mt$i" install
    local v; v=$(ver "$P"); expect "many-tags: '$spec'" "$want" "$v"
    [ "$v" = "$want" ] && expect "many-tags: '$spec' hello()" "consona-fx-many-tags@$want" "$(hello "$P")"
  done
  app mtjunk; npmi mtjunk install "$P@release-x"; local rc=$?
  ok "many-tags: junk tag release-x not a version" "install @release-x fails (ETARGET/no such tag), nothing installed" \
    bash -c "[ $rc -ne 0 ] && [ ! -d node_modules/$P ]"
  res "many-tags: @release-x message" INFO "-" "$(grep -m2 -i 'error' "$L/mtjunk.out" | tr '\n' ' ' | cut -c1-200)"
  app mtcli; npmi mtcli install "$P"
  expect "many-tags: 'npm install name' (no spec) saves" "^2.0.0" "$(node -p "require('./package.json').dependencies['$P']")"

  local N=$O/consona-fx-no-v-tags
  expect "no-v-tags: versions" '["1.0.0","1.0.1"]' "$(npm view "$N" versions --json 2>/dev/null | node -p 'JSON.stringify(JSON.parse(require("fs").readFileSync(0)))')"
  app nv1 "{\"$N\": \"^1.0.0\"}"; npmi nv1 install; expect "no-v-tags: '^1.0.0'" 1.0.1 "$(ver "$N")"
  app nv2 "{\"$N\": \"1.0.0\"}"; npmi nv2 install; expect "no-v-tags: exact 1.0.0" 1.0.0 "$(ver "$N")"

  local D=$O/consona-fx-default-branch-dev
  expect "default-branch-dev: versions" '["0.0.0-7b9727a"]' "$(npm view "$D" versions --json 2>/dev/null | node -p 'JSON.stringify([].concat(JSON.parse(require("fs").readFileSync(0))))')"
  app dbd1; npmi dbd1 install "$D"; expect "default-branch-dev: npm install name" 0.0.0-7b9727a "$(ver "$D")"
  expect "default-branch-dev: gitHead = develop HEAD" 7b9727a "$(npm view "$D" gitHead 2>/dev/null | cut -c1-7)"
  app dbd2 "{\"$D\": \"*\"}"; npmi dbd2 install; local v; v=$(ver "$D")
  if [ "$v" = 0.0.0-7b9727a ]; then res "default-branch-dev: '*' range" PASS "0.0.0-7b9727a" "$v"
  else res "default-branch-dev: '*' range" INFO "0.0.0-7b9727a (or npm ETARGET: '*' excludes prereleases)" "$v; $(grep -m1 -i 'notarget\|ETARGET\|error' "$L/dbd2.out" | cut -c1-160)"; fi

  local M=$O/consona-fx-name-mismatch
  app nm1; npmi nm1 install "$M@^1.0.0"; rc=$?
  local inst; inst=$(pjname "$M")
  res "name-mismatch: install by repo name" INFO \
    "clear error or warning; never silently under the wrong name" \
    "rc=$rc; installed at node_modules/$M; its package.json name=$inst (repo's says @$OWNER/something-else); npm output mentions something-else: $(grep -ci 'something-else' "$L/nm1.out"); daemon log mentions it: $(grep -ci 'something-else' "$CONSONA_HOME/daemon.log"); log store mentions it: $("$BIN" logs export --format jsonl 2>/dev/null | grep -ci 'something-else')"
  "$BIN" install "$M@^1.0.0" > "$L/nm1-consona.out" 2>&1
  local nmd nms nmc
  nmd=$(grep -c 'source.name_mismatch.*something-else' "$CONSONA_HOME/daemon.log")
  nms=$("$BIN" logs export --format jsonl 2>/dev/null | grep '"event":"source.name_mismatch"' | grep -c "\"package\":\"$M\".*something-else")
  nmc=$(grep -c '^consona: warning: .*something-else' "$L/nm1-consona.out")
  res "name-mismatch: warning shown" "$( [ "$nmd" -ge 1 ] && [ "$nms" -ge 1 ] && [ "$nmc" -ge 1 ] && echo PASS || echo FAIL)" \
    "a warning naming @$OWNER/something-else in daemon.log, the log store (source.name_mismatch) and the consona install summary" "daemon.log=$nmd log-store=$nms summary=$nmc"
  res "name-mismatch: summary text" INFO "-" "$(grep -m1 '^consona: warning:' "$L/nm1-consona.out" | cut -c1-300)"
  expect "name-mismatch: require(repo name) works (fixture hello() is hard-coded)" "something-else@1.0.0" "$(hello "$M")"
  app nm2; npmi nm2 install "$O/something-else"; rc=$?
  ok "name-mismatch: manifest name @$OWNER/something-else" "not installable (no such repo): clear 404" bash -c "[ $rc -ne 0 ] && grep -q '404' '$L/nm2.out'"
  lane_finish "consona-fx-many-tags consona-fx-no-v-tags consona-fx-default-branch-dev consona-fx-name-mismatch"
}

lane_graph() {
  lane_init graph 1
  local C1=$O/consona-fx-chain-1 C2=$O/consona-fx-chain-2 C3=$O/consona-fx-chain-3 LB=$O/consona-e2e-lib-b
  app chain "{\"$C1\": \"^1.0.0\"}"; npmi chain install; local rc=$?
  expect "chain: install chain-1 (rc, chain-2, chain-3)" "rc=0 1.0.0 1.0.0" "rc=$rc $(ver "$C2") $(ver "$C3")"
  expect "chain: lib-b version" 1.0.0 "$(ver "$LB")"
  local h; h=$(hello "$C1"); res "chain: hello()" "$(echo "$h" | grep -q 'chain-2@1.0.0 -> consona-fx-chain-3@1.0.0.*libB keys' && echo PASS || echo FAIL)" "chain-1 -> [chain-2 -> chain-3, libB keys]" "$h"
  local base; base=$(cpu_window 8); echo "$base" > "$L/cpu-before-cycle"
  res "daemon CPU idle after chain (8s window)" "$(awk -v c="$base" 'BEGIN{print (c<1.5)?"PASS":"FAIL"}')" "< 1.5 CPU-s" "${base} CPU-s"

  local CA=$O/consona-fx-cycle-a CB=$O/consona-fx-cycle-b
  app cycle "{\"$CA\": \"^1.0.0\"}"; local t=$(now); npmi cycle install; rc=$?
  ok "cycle: install terminates" "rc=0 within 300s ($(secs "$t" "$(now)")s)" bash -c "[ $rc -eq 0 ]"
  expect "cycle: each installed once" "1 1" "$(find node_modules -path "*consona-fx-cycle-a/package.json" | wc -l | tr -d ' ') $(find node_modules -path "*consona-fx-cycle-b/package.json" | wc -l | tr -d ' ')"
  expect "cycle: partner() both ways" "consona-fx-cycle-b@1.0.0 consona-fx-cycle-a@1.0.0" "$(node -e "console.log(require('$CA').partner()+' '+require('$CB').partner())" 2>&1)"
  sleep 2; local c1; c1=$(cpu_window 10); echo "$c1" > "$L/cpu-after-cycle"
  res "cycle: daemon idle after install (10s window)" "$(awk -v c="$c1" 'BEGIN{print (c<2)?"PASS":"FAIL"}')" "< 2 CPU-s (no background re-resolve loop)" "${c1} CPU-s"
  local n1; n1=$(grep -c 'source.resolve.*consona-fx-cycle-' "$CONSONA_HOME/daemon.log")
  sleep 35; local c2; c2=$(cpu_window 10); echo "$c2" > "$L/cpu-after-cycle-45s"
  res "cycle: daemon idle 45s later (10s window)" "$(awk -v c="$c2" 'BEGIN{print (c<2)?"PASS":"FAIL"}')" "< 2 CPU-s" "${c2} CPU-s"
  local n2; n2=$(grep -c 'source.resolve.*consona-fx-cycle-' "$CONSONA_HOME/daemon.log")
  expect "cycle: no background re-resolves after the install (source.resolve count over 45s)" "$n1 -> $n1" "$n1 -> $n2"
  res "cycle: background source.resolve events for cycle-a/b so far" INFO "a handful (one per install)" "$(grep -c 'source.resolve.*consona-fx-cycle-' "$CONSONA_HOME/daemon.log") events over ~$(secs "$t" "$(now)")s since the cycle install"

  local PE=$O/consona-fx-peer
  app peer1 "{\"$PE\": \"^1.0.0\", \"$LB\": \"^1.0.0\"}"; npmi peer1 install; rc=$?
  ok "peer: with lib-b in app" "rc=0, peer satisfied, getPeer() is lib-b" bash -c "[ $rc -eq 0 ] && cd '$L/apps/peer1' && node -e \"process.exit(require('$PE').getPeer() ? 0 : 1)\""
  app peer2 "{\"$PE\": \"^1.0.0\"}"; npmi peer2 install; rc=$?
  res "peer: without lib-b (npm 7+ default)" "$( [ $rc -eq 0 ] && [ "$(ver "$LB")" = 1.0.0 ] && echo PASS || echo FAIL)" "rc=0; npm auto-installs the private peer lib-b 1.0.0 via consona" "rc=$rc; lib-b=$(ver "$LB"); getPeer=$(node -e "console.log(!!require('$PE').getPeer())" 2>&1)"
  app peer3 "{\"$PE\": \"^1.0.0\"}"; npmi peer3 install --legacy-peer-deps; rc=$?
  res "peer: without lib-b, --legacy-peer-deps" "$( [ $rc -eq 0 ] && [ "$(ver "$LB")" = none ] && echo PASS || echo FAIL)" "rc=0; peer not installed; getPeer() null" "rc=$rc; lib-b=$(ver "$LB"); getPeer=$(node -e "console.log(!!require('$PE').getPeer())" 2>&1)"
  lane_finish "consona-fx-chain-1 consona-fx-chain-2 consona-fx-chain-3 consona-e2e-lib-b consona-fx-cycle-a consona-fx-cycle-b consona-fx-peer"
}

lane_pkg() {
  lane_init pkg 2
  local B=$O/consona-fx-bin
  app bin "{\"$B\": \"^1.0.0\"}"; npmi bin install
  expect "bin: node_modules/.bin/consona-fx-bin" "CONSONA_FX_BIN_OK v1.0.0" "$(./node_modules/.bin/consona-fx-bin 2>&1)"
  expect "bin: npx consona-fx-bin (installed)" "CONSONA_FX_BIN_OK v1.0.0" "$(npx --no-install consona-fx-bin 2>> "$L/npx.err" | tail -1)"
  ok "bin: target is executable" "bin/cli.js mode +x" test -x "node_modules/$B/bin/cli.js"
  mkdir -p "$L/apps/npxonly"; cd "$L/apps/npxonly"
  expect "bin: npx -y $B (not installed)" "CONSONA_FX_BIN_OK v1.0.0" "$(tmo 120 npx -y "$B" 2>> "$L/npx.err" | tail -1)"

  local E=$O/consona-fx-esm
  app esm "{\"$E\": \"^1.0.0\"}"; npmi esm install
  expect "esm: import { hello }" "consona-fx-esm@1.0.0" "$(node --input-type=module -e "import { hello } from '$E'; console.log(hello())" 2>&1)"
  local r; r=$(node -e "require('$E')" 2>&1 | grep -o 'ERR_[A-Z_]*' | head -1)
  expect "esm: require() fails" "ERR_PACKAGE_PATH_NOT_EXPORTED" "$r"

  local MR=$O/consona-fx-monorepo-root
  app mono "{\"$MR\": \"^1.0.0\"}"; npmi mono install
  local files; files=$(cd "node_modules/$MR" 2>/dev/null && find . -type f | sed 's|^\./||' | sort | tr '\n' ' ' | sed 's/ $//')
  res "monorepo-root: installed files" "$(echo " $files " | grep -q ' packages/' && echo FAIL || echo PASS)" "root only (index.js, package.json, README), no packages/inner" "$files"
  expect "monorepo-root: hello()" ok "$(node -e "require('$MR'); console.log('ok')" 2>&1)"
  app mono2; npmi mono2 install "$O/consona-fx-monorepo-inner"; local rc=$?
  ok "monorepo-root: inner package by its own name" "not resolvable: clear 404 (no such repo)" bash -c "[ $rc -ne 0 ] && grep -q 404 '$L/mono2.out'"
  res "monorepo-root: inner 404 message" INFO "-" "$(grep -m1 '404' "$L/mono2.out" | cut -c1-220)"

  local LF=$O/consona-fx-large-files
  app large "{\"$LF\": \"^1.0.0\"}"; npmi large install
  local tl; tl=$(cd "$L/apps/large" && tarlist "$LF"); local sz; sz=$(fsize "$(cat "$L/tmp/last-tgz")")
  res "large-files: tarball" "$(echo "$tl" | grep -q big.bin && echo FAIL || { echo "$tl" | grep -q 'package/assets/small.txt' && [ "$sz" -lt 20000 ] && echo PASS || echo FAIL; })" "excludes assets/big.bin, includes assets/small.txt, ~1 KB" "$sz bytes: $tl"
  res "large-files: hello()" "$( [ -n "$(hello "$LF")" ] && ! hello "$LF" | grep -q Error && echo PASS || echo FAIL)" "small.txt contents" "$(hello "$LF")"

  local NI=$O/consona-fx-npmignore
  app npmignore "{\"$NI\": \"^1.0.0\"}"; npmi npmignore install
  expect "npmignore: tarball contents" "package/README.md package/index.js package/lib/util.js package/package.json" "$(tarlist "$NI")"

  local PB=$O/consona-fx-prepare-build
  app prep "{\"$PB\": \"^1.0.0\"}"; npmi prep install; rc=$?
  ok "prepare-build: install" "rc=0" test $rc -eq 0
  expect "prepare-build: lockfile integrity on the first install" "sha512" "$(node -p 'String(require("./package-lock.json").packages["node_modules/'"$PB"'"].integrity||"MISSING").slice(0,6)' 2>&1)"
  expect "prepare-build: dist/BUILD_STAMP.txt (prepack ran)" prepack-ran "$(cat "node_modules/$PB/dist/BUILD_STAMP.txt" 2>&1)"
  expect "prepare-build: hello() from dist/index.js" prepare-build@1.0.0 "$(hello "$PB")"
  expect "prepare-build: tarball contents" "package/README.md package/dist/BUILD_STAMP.txt package/dist/index.js package/package.json" "$(tarlist "$PB")"
  ok "prepare-build: build.start/finish logged" "build.finish ok in logs" bash -c "'$BIN' logs --event build --json --limit 50 2>/dev/null | grep -q 'consona-fx-prepare-build'"

  local BF=$O/consona-fx-build-fails AR=$O/consona-fx-archived
  app bf "{\"$AR\": \"^1.0.0\", \"$BF\": \"^1.0.0\"}"; cp package.json "$L/bf-package.json.orig"
  npmi bf install; rc=$?
  ok "build-fails: npm install fails" "rc != 0" test $rc -ne 0
  local msg; msg=$(grep -i 'build failed\|422' "$L/bf.out" | head -3 | tr '\n' ' ' | cut -c1-400)
  res "build-fails: npm error text" INFO "-" "$msg"
  ok "build-fails: names package@version" "error names $BF@1.0.0" grep -q "$BF@1.0.0" "$L/bf.out"
  ok "build-fails: names the failing script" "npm error says the \"prepare\" script failed" grep -q "the package's \"prepare\" script failed" "$L/bf.out"
  if grep -q 'CONSONA_FX_BUILD_FAILS: deliberate prepare failure' "$L/bf.out"; then res "build-fails: script's stderr in npm error" PASS "includes CONSONA_FX_BUILD_FAILS: deliberate prepare failure" "included"
  else res "build-fails: script's stderr in npm error" FAIL "includes CONSONA_FX_BUILD_FAILS: deliberate prepare failure" "not included; only a path to build-failed.log"; fi
  local logf; logf=$(grep -o '/[^ "]*build-failed.log' "$L/bf.out" | head -1)
  ok "build-fails: build-failed.log has the script's stderr" "log file contains the message" grep -q 'CONSONA_FX_BUILD_FAILS: deliberate prepare failure' "${logf:-/nonexistent}"
  ok "build-fails: no partial install" "no node_modules entries, no lockfile, package.json unchanged" \
    bash -c "[ ! -e node_modules/$BF ] && [ ! -e node_modules/$AR ] && [ ! -e package-lock.json ] && cmp -s package.json '$L/bf-package.json.orig'"
  ok "build-fails: no crash stack" "no panic/goroutine in npm output or daemon log" bash -c "! grep -qi 'panic\|goroutine ' '$L/bf.out' '$CONSONA_HOME/daemon.log'"
  "$BIN" install > "$L/bf-consona.out" 2>&1; rc=$?
  ok "build-fails: consona install summary" "rc != 0 and a 'consona: build failed for $BF@1.0.0' line" bash -c "[ $rc -ne 0 ] && grep -q '^consona: build failed for $BF@1.0.0' '$L/bf-consona.out'"
  ok "build-fails: consona install summary has the script's stderr" "includes CONSONA_FX_BUILD_FAILS: deliberate prepare failure" grep -q 'CONSONA_FX_BUILD_FAILS: deliberate prepare failure' "$L/bf-consona.out"
  res "build-fails: consona install text" INFO "-" "$(grep '^consona:' "$L/bf-consona.out" | head -2 | tr '\n' ' ' | cut -c1-400)"
  lane_finish "consona-fx-bin consona-fx-esm consona-fx-monorepo-root consona-fx-large-files consona-fx-npmignore consona-fx-prepare-build consona-fx-build-fails consona-fx-archived"
}

lane_repo() {
  lane_init repo 3
  local AR=$O/consona-fx-archived
  app arch "{\"$AR\": \"^1.0.0\"}"; npmi arch install; local rc=$?
  expect "archived: installable" "rc=0 1.0.0 consona-fx-archived" "rc=$rc $(ver "$AR") $(hello "$AR" | grep -o 'consona-fx-archived' | head -1)"
  res "archived: hello()" INFO "-" "$(hello "$AR")"
  local RO=$O/consona-fx-renamed-old RN=$O/consona-fx-renamed-new
  app ren "{\"$RO\": \"^1.0.0\"}"; npmi ren install; rc=$?
  res "renamed: install via old name" "$( [ $rc -eq 0 ] && [ "$(ver "$RO")" = 1.0.0 ] && echo PASS || echo FAIL)" "follows GitHub's redirect: 1.0.0 installed as $RO" "rc=$rc; version=$(ver "$RO"); hello=$(hello "$RO"); $(grep -m1 -i 'err' "$L/ren.out" | cut -c1-160)"
  "$BIN" install "$RO@^1.0.0" > "$L/ren-consona.out" 2>&1
  local rd rs rc2
  rd=$(grep -c "source.redirect.*consona-fx-renamed-old.*consona-fx-renamed-new" "$CONSONA_HOME/daemon.log")
  rs=$("$BIN" logs export --format jsonl 2>/dev/null | grep '"event":"source.redirect"' | grep -c 'consona-fx-renamed-new')
  rc2=$(grep -c '^consona: warning: .*consona-fx-renamed-old was renamed or transferred to github.com/[^ ]*consona-fx-renamed-new' "$L/ren-consona.out")
  res "renamed: redirect warning (old name)" "$( [ "$rd" -ge 1 ] && [ "$rs" -ge 1 ] && [ "$rc2" -ge 1 ] && echo PASS || echo FAIL)" \
    "warning naming the new repo in daemon.log, the log store (source.redirect) and the consona install summary" "daemon.log=$rd log-store=$rs summary=$rc2"
  app ren2 "{\"$RN\": \"^1.0.0\"}"; npmi ren2 install; rc=$?
  res "renamed: install via new name" INFO "record" "rc=$rc; version=$(ver "$RN"); package.json name=$(pjname "$RN") (repo manifest says $RO)"
  ok "renamed: new name, manifest still says the old name -> name_mismatch warning" "source.name_mismatch for $RN in the log store" \
    bash -c "'$BIN' logs export --format jsonl 2>/dev/null | grep '\"event\":\"source.name_mismatch\"' | grep -q '\"package\":\"$RN\"'"

  local TM=$O/consona-fx-tag-moved A=4082980268199cb692fbf7e90bb1f13acec7dd6f B=cd0cc0bf3dff906caaedf535051ef3585b5bdd64
  app tm "{\"$TM\": \"1.0.0\"}"; npmi tm install; rc=$?
  expect "tag-moved: fresh resolve gets B" "tag-moved B" "$(hello "$TM")"
  expect "tag-moved: gitHead" "$B" "$(npm view "$TM" gitHead 2>/dev/null)"
  # A lockfile written before the move: same version and URL, A's tarball integrity.
  mkdir -p "$L/tmA/src" "$L/tmA/pack"
  ghapi "/repos/$OWNER/consona-fx-tag-moved/tarball/$A" > "$L/tmA/a.tgz" && tar xzf "$L/tmA/a.tgz" -C "$L/tmA/src" --strip-components=1 \
    && (cd "$L/tmA/src" && npm pack --ignore-scripts --silent --pack-destination "$L/tmA/pack" >/dev/null 2>&1)
  local AT; AT=$(ls "$L/tmA/pack"/*.tgz 2>/dev/null | head -1)
  ok "tag-moved: rebuilt commit A's tarball (for the old lockfile)" "A's package says 'tag-moved A'" bash -c "[ -n '$AT' ] && tar xzOf '$AT' package/index.js | grep -q 'tag-moved A'"
  local IA IB; IA=sha512-$(sha512b64 "$AT"); IB=$(node -p "require('./package-lock.json').packages['node_modules/$TM'].integrity")
  res "tag-moved: integrity A vs B" "$( [ "$IA" != "$IB" ] && echo PASS || echo FAIL)" "differ" "A=${IA:0:20}… B=${IB:0:20}…"
  node -e 'const f="package-lock.json",l=JSON.parse(require("fs").readFileSync(f));l.packages["node_modules/"+process.argv[1]].integrity=process.argv[2];require("fs").writeFileSync(f,JSON.stringify(l,null,2))' "$TM" "$IA"
  rm -rf node_modules; npm cache clean --force >/dev/null 2>&1
  npmi tm-ci-A ci; rc=$?
  res "tag-moved: npm ci with lockfile pinned to A" "$( [ $rc -ne 0 ] && grep -q EINTEGRITY "$L/tm-ci-A.out" && echo PASS || echo FAIL)" "clear integrity failure (EINTEGRITY) or refetch of A" "rc=$rc; $(grep -m2 -i 'EINTEGRITY\|integrity\|error' "$L/tm-ci-A.out" | tr '\n' ' ' | cut -c1-300); installed=$(ver "$TM")"
  ok "tag-moved: nothing installed after the failure" "no node_modules/$TM" test ! -e "node_modules/$TM"
  lane_finish "consona-fx-archived consona-fx-renamed-old consona-fx-renamed-new consona-fx-tag-moved"
}

if command -v sha256sum >/dev/null; then SHA256=sha256sum; else SHA256="shasum -a 256"; fi

lane_lock() {
  lane_init lock 4
  local MT=$O/consona-fx-many-tags C1=$O/consona-fx-chain-1 PB=$O/consona-fx-prepare-build E=$O/consona-fx-esm B=$O/consona-fx-bin NV=$O/consona-fx-no-v-tags
  app mix "{\"$MT\": \"^1.0.0\", \"$C1\": \"^1.0.0\", \"$PB\": \"^1.0.0\", \"$E\": \"^1.0.0\", \"$B\": \"^1.0.0\", \"$NV\": \"^1.0.0\"}"
  local t=$(now); "$BIN" install > "$L/mix-consona.out" 2>&1; local rc=$?; echo "consona-install-mix $(secs "$t" "$(now)")s rc=$rc" >> "$L/timings.txt"
  ok "lock: consona install of a mixed app" "rc=0" test $rc -eq 0
  res "lock: consona install source lines" INFO "-" "$(grep -c '<- github' "$L/mix-consona.out") '<- github' lines"
  node -e '
    const l=require("./package-lock.json"), port=process.argv[1], o=process.argv[2]; let bad=[], n=0;
    for (const [k,v] of Object.entries(l.packages)) { if (!k.includes(o+"/")) continue; n++;
      if (!(v.resolved||"").startsWith("http://127.0.0.1:"+port+"/npm/"+o+"/") || !/^sha512-/.test(v.integrity||"")) bad.push(k+" resolved="+v.resolved+" integrity="+(v.integrity||"MISSING")); }
    const txt=JSON.stringify(l); if (/registry\.npmjs\.org|github\.com|codeload/.test(txt)) bad.push("public/github URL in lockfile");
    console.log("bad: "+(bad.join("; ")||"none")+" ("+n+" private entries)")' "$PORT" "$O" > "$L/lock-check.out" 2>&1
  cp package-lock.json "$L/mix-package-lock.json"; local lc; lc=$(cat "$L/lock-check.out")
  res "lock: private entries -> 127.0.0.1:$PORT only, with integrity" "$(echo "$lc" | grep -q '^bad: none (9 private' && echo PASS || echo FAIL)" "bad: none (9 private entries)" "$lc"
  cp package-lock.json "$L/lock.orig.json"; local before; before=$(npm ls --all --json 2>/dev/null | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const w=(d,a)=>{for(const[k,v] of Object.entries(d.dependencies||{})){a.push(k+"@"+v.version);w(v,a)}return a};console.log(w(JSON.parse(s),[]).sort().join(" "))})')
  rm -rf node_modules; npmi ci-warm ci; rc=$?
  local after; after=$(npm ls --all --json 2>/dev/null | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const w=(d,a)=>{for(const[k,v] of Object.entries(d.dependencies||{})){a.push(k+"@"+v.version);w(v,a)}return a};console.log(w(JSON.parse(s),[]).sort().join(" "))})')
  ok "lock: reinstall from lockfile (npm ci, warm)" "rc=0, same tree, lockfile unchanged" bash -c "[ $rc -eq 0 ] && [ '$before' = '$after' ] && cmp -s package-lock.json '$L/lock.orig.json'"
  # Cold: new daemon with an empty consona cache and an empty npm cache: every
  # private tarball (incl. the prepare-build one) is rebuilt and must match the lockfile.
  "$BIN" daemon stop >/dev/null 2>&1; mv "$CONSONA_HOME" "$L/consona-home.warm"; rm -rf "$L/npm-cache" node_modules
  "$BIN" daemon start > "$L/restart.out" 2>&1
  npmi ci-cold ci; rc=$?
  ok "lock: npm ci with empty consona + npm caches" "rc=0 (rebuilt tarballs match lockfile integrity)" bash -c "[ $rc -eq 0 ] && [ -f node_modules/$PB/dist/BUILD_STAMP.txt ]"
  [ $rc -ne 0 ] && res "lock: cold ci error" INFO "-" "$(grep -m2 -i 'EINTEGRITY\|error' "$L/ci-cold.out" | tr '\n' ' ' | cut -c1-300)"
  # Missing-integrity follow-ups: is the rebuilt prepare-build tarball byte-identical
  # (warm vs cold daemon), and does a later plain `npm install` fill the hash in?
  local tw tc; tw=$(find "$L/consona-home.warm" -path '*consona-fx-prepare-build*' -name '*.tgz' -exec $SHA256 {} + 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')
  tc=$(find "$CONSONA_HOME" -path '*consona-fx-prepare-build*' -name '*.tgz' -exec $SHA256 {} + 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')
  res "lock: prepare-build tarball deterministic (warm vs cold build)" "$( [ -n "$tw" ] && [ "$tw" = "$tc" ] && echo PASS || echo FAIL)" "same sha256" "warm=${tw:0:16} cold=${tc:0:16}"
  npmi reinstall install; rc=$?
  expect "lock: prepare-build integrity after a 2nd npm install (unchanged)" "rc=0 $(node -p 'require(process.argv[1]).packages["node_modules/'"$PB"'"].integrity||"MISSING"' "$L/lock.orig.json" | cut -c1-24)" "rc=$rc $(node -p 'require("./package-lock.json").packages["node_modules/'"$PB"'"].integrity||"MISSING"' | cut -c1-24)"
  npmi audit-real audit --json; rc=$?
  local vul; vul=$(grep -v '^npm \|^consona: ' "$L/audit-real.out" | node -p 'const a=JSON.parse(require("fs").readFileSync(0,"utf8"));JSON.stringify(a.metadata&&a.metadata.vulnerabilities&&a.metadata.vulnerabilities.total)' 2>/dev/null)  # drop npm/consona log lines around the JSON
  res "audit: npm audit (registry.npmjs.org upstream)" "$( [ $rc -eq 0 ] && [ "$vul" = 0 ] && echo PASS || echo FAIL)" "rc=0, 0 vulnerabilities, private names withheld" "rc=$rc total=$vul; $("$BIN" logs --event audit --limit 5 2>/dev/null | grep -o 'forwarding [0-9]* public package names, withheld [0-9]* private' | tail -1)"
  lane_finish "consona-fx-many-tags consona-fx-chain-1 consona-fx-chain-2 consona-fx-chain-3 consona-e2e-lib-b consona-fx-prepare-build consona-fx-esm consona-fx-bin consona-fx-no-v-tags"
}

lane_audit() {
  local CP=$((BASE+9))
  # A local stand-in for the public registry that records every request body.
  cat > "$TMP/capture.py" <<'PY'
import http.server, sys, json
log = open(sys.argv[2], "a")
class H(http.server.BaseHTTPRequestHandler):
    def _h(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n).decode("utf-8", "replace") if n else ""
        log.write(json.dumps({"method": self.command, "path": self.path, "body": body}) + "\n"); log.flush()
        code, out = (200, b"{}") if "/security/" in self.path else (404, b'{"error":"not found"}')
        self.send_response(code); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(out))); self.end_headers(); self.wfile.write(out)
    do_GET = do_POST = do_PUT = do_HEAD = _h
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
  : > "$TMP/capture.log"; env -u "$OWNER_ENV" python3 "$TMP/capture.py" "$CP" "$TMP/capture.log" & echo $! > "$TMP/capture.pid"
  for _ in $(seq 50); do curl -s "http://127.0.0.1:$CP/" >/dev/null 2>&1 && break; sleep 0.1; done
  lane_init audit 5 "http://127.0.0.1:$CP/"
  local C1=$O/consona-fx-chain-1 PE=$O/consona-fx-peer
  app au "{\"$C1\": \"^1.0.0\", \"$PE\": \"^1.0.0\"}"; npmi au-install install; local rc=$?
  ok "audit: install private-only app (upstream = capture)" "rc=0" test $rc -eq 0
  npmi au-audit audit --json; rc=$?
  res "audit: npm audit, private-only tree" "$( [ $rc -eq 0 ] && echo PASS || echo FAIL)" "rc=0; nothing private sent upstream" "rc=$rc; $(one "$L/au-audit.out" 160)"
  # Mixed bulk payload (what npm sends when a tree also has public packages).
  printf '{"%s":["1.0.0"],"%s":["1.0.0"],"%s/consona-e2e-lib-b":["1.0.0"],"lodash":["4.17.21"]}' "$C1" "$PE" "$O" \
    | curl -s -X POST -H 'Content-Type: application/json' --data-binary @- "http://127.0.0.1:$PORT/npm/-/npm/v1/security/advisories/bulk" > "$L/bulk.out"
  local fx; fx=$(grep -ci "consona-fx\|consona-e2e\|$OWNER" "$TMP/capture.log")
  expect "audit: upstream never sees a private name" 0 "$fx"
  ok "audit: public names still forwarded (bulk, lodash)" "capture got the bulk request with lodash" grep -q 'advisories/bulk.*lodash' "$TMP/capture.log"
  res "audit: upstream requests" INFO "-" "$(node -e 'console.log(require("fs").readFileSync(process.argv[1],"utf8").split("\n").filter(Boolean).map(l=>{const j=JSON.parse(l);return j.method+" "+j.path}).join("; "))' "$TMP/capture.log")"
  lane_finish "consona-fx-chain-1 consona-fx-chain-2 consona-fx-chain-3 consona-e2e-lib-b consona-fx-peer"
}

# ---------- run ----------
echo "edge: consona $( "$BIN" version ) on $OS $(uname -m); node $(node -v); npm $(npm -v); lanes: $LANES; ports $BASE-$((BASE+9))"
for ln in $LANES; do
  ( "lane_$ln" ) > "$TMP/$ln.out" 2>&1 &
  echo "$!" >> "$TMP/lane-pids"
done
wait
for ln in $LANES; do echo "===== lane $ln"; cat "$TMP/$ln.out"; done
cat "$TMP"/lane-*/results.tsv > "$TMP/results.tsv" 2>/dev/null
NP=$(awk -F'\t' '$3=="PASS"' "$TMP/results.tsv" | wc -l | tr -d ' ')
NF=$(awk -F'\t' '$3=="FAIL"' "$TMP/results.tsv" | wc -l | tr -d ' ')
NI=$(awk -F'\t' '$3=="INFO"' "$TMP/results.tsv" | wc -l | tr -d ' ')
echo "===== guards"
GHN=$(wc -l < "$TMP/gh-calls.log" | tr -d ' ')
[ "$GHN" -eq 0 ] && echo "PASS  gh tripwire: never called" || { echo "FAIL  gh tripwire called $GHN times"; NF=$((NF+1)); }
LEAK=$(grep -rlF -- "$TOK" "$TMP" 2>/dev/null | wc -l | tr -d ' ')
PAT=$(grep -rlE 'gh[opsu]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}' "$TMP" 2>/dev/null | wc -l | tr -d ' ')
[ "$LEAK" -eq 0 ] && [ "$PAT" -eq 0 ] && echo "PASS  token scan: 0 files with the token value, 0 token-shaped strings" || { echo "FAIL  token scan: value in $LEAK files, patterns in $PAT files"; NF=$((NF+1)); }
echo "===== timings"; for ln in $LANES; do sed "s/^/$ln: /" "$TMP/lane-$ln/timings.txt" 2>/dev/null; done
echo "total $(secs "$T0" "$(now)")s"
if [ -n "${EDGE_OUT:-}" ]; then
  mkdir -p "$EDGE_OUT"; cp "$TMP/results.tsv" "$TMP"/*.out "$TMP/gh-calls.log" "$EDGE_OUT/" 2>/dev/null
  for d in "$TMP"/lane-*; do n=$(basename "$d"); mkdir -p "$EDGE_OUT/$n"; cp "$d"/*.out "$d"/*.txt "$d"/*.tsv "$d"/logs.jsonl "$d"/daemon.log "$d"/cpu-* "$EDGE_OUT/$n/" 2>/dev/null; done
  [ -f "$TMP/capture.log" ] && cp "$TMP/capture.log" "$EDGE_OUT/"
fi
unset TOK
echo "EDGE: $NP pass, $NF fail, $NI info"
[ "$NF" -eq 0 ]
