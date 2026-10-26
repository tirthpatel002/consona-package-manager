#!/usr/bin/env bash
# End-to-end: flexible GitHub credentials (e2e/run.sh covers the rest).
# Scenarios, each with an isolated HOME, consona home and npmrc:
#   A  no gh on PATH + a named token in the OS secure store (Linux: a
#      throwaway gnome-keyring inside dbus-run-session)
#   B  CONSONA_TOKEN env only (no gh, no secure store)
#   C  gh login only (GH_CONFIG_DIR)
#   D  mixed: a rejected CONSONA_TOKEN_<OWNER> falls through to the gh login
#   E  every credential rejected: one error listing each source and its fix
#   F  no secure store: `token add` fails clearly and writes nothing
# Needs: go, node/npm, gh with a login that can read the fixtures, given by
# E2E_GH_CONFIG_DIR (the token is read into this process's memory only).
# Scenario A on Linux needs dbus-run-session, gnome-keyring-daemon and
# secret-tool (KEYRING_ROOT=<extracted debs> works without installing).
set -uo pipefail
set +x   # never trace: TOK is a real token
OWNER=${OWNER:-rakesh-jain-cb1998}
OWNER_ENV=CONSONA_TOKEN_$(printf '%s' "$OWNER" | tr 'a-z-' 'A-Z_')
ROOT=$(cd "$(dirname "$0")/.." && pwd)
PORT=${CONSONA_PORT:-48745}
OS=$(uname -s)
FAILS=0 PASSES=0
pass() { printf 'PASS  %s\n' "$*"; PASSES=$((PASSES+1)); }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS+1)); }
check() { local msg=$1; shift; if "$@"; then pass "$msg"; else fail "$msg"; fi; }
show() { sed 's/^/      | /' "$@"; }
if command -v perl >/dev/null 2>&1; then now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }
else now() { python3 -c 'import time; print("%.3f" % time.time())'; }; fi
secs() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", b-a}'; }

# ---- scenario A body (re-invoked inside dbus-run-session) ----
scenario_a() {
  set -uo pipefail
  if [ "$OS" != Darwin ]; then
    printf x | gnome-keyring-daemon --unlock --components=secrets --daemonize >/dev/null 2>&1 \
      || { fail "A: could not start gnome-keyring-daemon"; return; }
  fi
  local H=$TMP/a-home; mkdir -p "$H"
  ca() { env -i HOME="$H" PATH="$NOGH" USER="${USER:-e2e}" LANG=C.UTF-8 TMPDIR="$TMP" XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-}" \
           DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" LD_LIBRARY_PATH="${LD_LIBRARY_PATH:-}" \
           CONSONA_HOME="$TMP/a-consona" CONSONA_PORT="$PORT" npm_config_userconfig="$TMP/a.npmrc" npm_config_update_notifier=false npm_config_fund=false "$@"; }
  check "A: gh is not on PATH" bash -c "! PATH='$NOGH' command -v gh >/dev/null"
  printf '%s' "$TOK" | ca "$BIN" token add rakesh --for "@$OWNER" --description "e2e fixture owner" > "$TMP/a-add.out" 2>&1; rc=$?
  show "$TMP/a-add.out"
  check "A: token add (piped stdin) rc=$rc, account $OWNER, value masked" bash -c "[ $rc -eq 0 ] && grep -q 'account:     $OWNER' '$TMP/a-add.out' && grep -qE '\((gh[a-z]_|github_pat_)…[A-Za-z0-9_]{4}\)' '$TMP/a-add.out'"
  check "A: probes for @$OWNER all PASS (repos, metadata, contents, tags)" bash -c "[ \$(grep -c '^  PASS  @$OWNER' '$TMP/a-add.out') -ge 4 ] && ! grep -q '^  FAIL' '$TMP/a-add.out'"
  local IDX=$H/.config/consona/tokens.json
  [ "$OS" = Darwin ] && IDX="$H/Library/Application Support/consona/tokens.json"
  local fm dm
  fm=$(stat -c %a "$IDX" 2>/dev/null || stat -f %Lp "$IDX"); dm=$(stat -c %a "$(dirname "$IDX")" 2>/dev/null || stat -f %Lp "$(dirname "$IDX")")
  check "A: index file mode $fm (want 600), dir mode $dm (want 700)" test "$fm" = 600 -a "$dm" = 700
  check "A: index has no token value" bash -c "! grep -qF -f <(printf '%s\n' \"\$1\") '$IDX'" _ "$TOK"
  if [ "$OS" != Darwin ]; then
    local got; got=$(ca secret-tool lookup service dev.consona.tokens username rakesh 2>/dev/null)
    check "A: value is in the Secret Service (service dev.consona.tokens)" test "$got" = "$TOK"; unset got
  fi
  printf '%s' "$TOK" | ca "$BIN" token add rakesh > "$TMP/a-dup.out" 2>&1; rc=$?
  check "A: adding the same name again fails without --replace (rc=$rc)" bash -c "[ $rc -ne 0 ] && grep -q -- '--replace' '$TMP/a-dup.out'"
  ca "$BIN" token add other --token=not-a-real-token > "$TMP/a-argv.out" 2>&1; rc=$?
  check "A: --token= on the command line refused (rc=$rc)" bash -c "[ $rc -ne 0 ] && grep -q 'refusing a token on the command line' '$TMP/a-argv.out'"
  ca "$BIN" token list > "$TMP/a-list.out" 2>&1; show "$TMP/a-list.out"
  check "A: token list shows name/owner/login/type, no value" bash -c "grep -q '^rakesh' '$TMP/a-list.out' && grep -q '@$OWNER' '$TMP/a-list.out' && grep -q 'e2e fixture owner' '$TMP/a-list.out'"
  ca "$BIN" token test rakesh > "$TMP/a-test.out" 2>&1; rc=$?
  check "A: token test rakesh rc=$rc" bash -c "[ $rc -eq 0 ] && grep -q '^  PASS  GET /user' '$TMP/a-test.out'"
  (cd "$H" && ca "$BIN" setup --no-autostart --scope "@$OWNER" > "$TMP/a-setup.out" 2>&1); rc=$?
  show "$TMP/a-setup.out"
  check "A: setup without gh rc=$rc; no '@@' in output or npmrc" bash -c "[ $rc -eq 0 ] && ! grep -q '@@' '$TMP/a-setup.out' '$TMP/a.npmrc' && grep -q '^@$OWNER:registry=http://127.0.0.1:$PORT/npm/' '$TMP/a.npmrc'"
  mkdir -p "$H/app"; cp "$TMP/app.json" "$H/app/package.json"
  t0=$(now); (cd "$H/app" && ca "$BIN" install > "$TMP/a-install.out" 2>&1); rc=$?; t1=$(now); A_T=$(secs $t0 $t1)
  check "A: install of lib-a/b/c/ts with the named token, no gh [${A_T}s] rc=$rc" bash -c "[ $rc -eq 0 ] && ls -d '$H'/app/node_modules/@$OWNER/consona-e2e-lib-{a,b,c,ts} >/dev/null"
  OUT=$(cd "$H/app" && ca node -e "console.log(require('@$OWNER/consona-e2e-lib-a').greet('e2e'), require('@$OWNER/consona-e2e-lib-ts').slugify(' A B '))" 2>&1)
  check "A: private-of-private + built TS work: $OUT" test "$OUT" = "HELLO E2E! a-b"
  ca "$BIN" logs --event credential > "$TMP/a-logs.out" 2>&1; show "$TMP/a-logs.out"
  check "A: log store: credential.resolve names the named token" grep -q 'credential.resolve.*named token \\\?"rakesh' "$TMP/a-logs.out"
  ca "$BIN" logs info > "$TMP/a-loginfo.out" 2>&1; show "$TMP/a-loginfo.out"
  check "A: logs info: sqlite backend with entries" bash -c "grep -q '^backend:  sqlite' '$TMP/a-loginfo.out' && ! grep -q '^entries:  0$' '$TMP/a-loginfo.out'"
  ca "$BIN" logs export --format jsonl -o "$TMP/a-export.jsonl" > /dev/null 2>&1
  check "A: logs export wrote JSON lines with build + install events" bash -c "grep -q '\"event\":\"build.finish\"' '$TMP/a-export.jsonl' && grep -q '\"event\":\"install.resolve\"' '$TMP/a-export.jsonl'"
  ca "$BIN" daemon stop > /dev/null 2>&1
  ca "$BIN" token remove rakesh > "$TMP/a-rm.out" 2>&1; rc=$?
  if [ "$OS" != Darwin ]; then
    check "A: token remove rc=$rc; gone from the Secret Service and the index" bash -c "[ $rc -eq 0 ] && [ -z \"\$(secret-tool lookup service dev.consona.tokens username rakesh 2>/dev/null)\" ] && ! grep -q rakesh '$IDX'"
  fi
  (cd "$H" && ca "$BIN" setup --undo > /dev/null 2>&1)
}

if [ "${1:-}" = __scenario_a ]; then
  scenario_a
  echo "A_RESULT $PASSES $FAILS ${A_T:-}"
  exit 0
fi

# ---- setup ----
for c in go node npm gh curl; do command -v $c >/dev/null || { echo "creds e2e: $c not on PATH"; exit 1; }; done
: "${E2E_GH_CONFIG_DIR:?set E2E_GH_CONFIG_DIR to the gh config dir of the fixture owner}"
LOGIN=$(GH_CONFIG_DIR=$E2E_GH_CONFIG_DIR gh api user --jq .login 2>/dev/null)
[ "$LOGIN" = "$OWNER" ] || { echo "creds e2e: gh login in $E2E_GH_CONFIG_DIR is '$LOGIN', expected '$OWNER'"; exit 1; }
TOK=$(GH_CONFIG_DIR=$E2E_GH_CONFIG_DIR gh auth token 2>/dev/null) ||
  TOK=$(GH_CONFIG_DIR=$E2E_GH_CONFIG_DIR gh config get -h github.com oauth_token 2>/dev/null)   # gh < 2.17
[ -n "$TOK" ] || { echo "creds e2e: no token from gh in $E2E_GH_CONFIG_DIR"; exit 1; }
GARBAGE="ghp_$(od -An -N18 -tx1 /dev/urandom | tr -d ' \n')"   # well-formed, never valid
TMP=$(mktemp -d /tmp/consona-creds-e2e.XXXXXX)
BIN=$TMP/bin/consona
KEEP=
cleanup() {
  for h in "$TMP"/*-consona; do [ -d "$h" ] && CONSONA_HOME=$h CONSONA_PORT=$PORT "$BIN" daemon stop >/dev/null 2>&1; done
  rm -rf "$TMP"/a-home/.local/share/keyrings   # the throwaway keyring, even when keeping the rest
  [ -z "$KEEP" ] && rm -rf "$TMP"
}
trap cleanup EXIT
(cd "$ROOT" && go build -o "$BIN" ./cmd/consona) || { echo "build failed"; exit 1; }
# A PATH with everything on the current PATH except gh.
NOGH=$TMP/nogh; mkdir -p "$NOGH"
IFS=: read -ra DIRS <<< "$PATH"
for d in "${DIRS[@]}"; do
  for f in "$d"/*; do
    n=${f##*/}; [ "$n" = gh ] && continue
    [ -x "$f" ] && [ ! -e "$NOGH/$n" ] && ln -s "$f" "$NOGH/$n"
  done
done
cat > "$TMP/app.json" <<J
{ "name": "creds-app", "private": true, "version": "0.0.0",
  "dependencies": { "@$OWNER/consona-e2e-lib-a": "^1.0.0", "@$OWNER/consona-e2e-lib-c": "*", "@$OWNER/consona-e2e-lib-ts": "^1.0.0", "clsx": "^2.1.1" } }
J
export OWNER OWNER_ENV ROOT PORT OS TMP BIN NOGH TOK

# Isolated environment for scenarios B-F: HOME, consona home, npmrc.
run_in() {   # run_in NAME PATH [VAR=val ...] -- cmd...
  local name=$1 path=$2; shift 2
  local vars=(); while [ "$1" != -- ]; do vars+=("$1"); shift; done; shift
  mkdir -p "$TMP/$name-home"
  env -i HOME="$TMP/$name-home" PATH="$path" USER="${USER:-e2e}" LANG=C.UTF-8 TMPDIR="$TMP" \
    DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent/consona-e2e \
    CONSONA_HOME="$TMP/$name-consona" CONSONA_PORT="$PORT" npm_config_userconfig="$TMP/$name.npmrc" \
    npm_config_update_notifier=false npm_config_fund=false "${vars[@]}" "$@"
}
install_in() {   # install_in NAME PATH VARS... : setup + install the app, prints seconds
  local name=$1 path=$2; shift 2
  mkdir -p "$TMP/$name-home/app"; cp "$TMP/app.json" "$TMP/$name-home/app/package.json"
  (cd "$TMP/$name-home" && run_in "$name" "$path" "$@" -- "$BIN" setup --no-autostart --scope "$OWNER" > "$TMP/$name-setup.out" 2>&1)
  local t0 t1 rc; t0=$(now)
  (cd "$TMP/$name-home/app" && run_in "$name" "$path" "$@" -- "$BIN" install > "$TMP/$name-install.out" 2>&1); rc=$?
  t1=$(now); echo "$rc $(secs $t0 $t1)"
}
install_ok() { [ "$1" -eq 0 ] && installed "$2"; }
installed() { ls -d "$TMP/$1-home"/app/node_modules/@$OWNER/consona-e2e-lib-{a,b,c,ts} >/dev/null 2>&1; }
finish() {   # stop the scenario's daemon and undo its npm setup
  (cd "$TMP/$1-home" && run_in "$@" -- "$BIN" setup --undo >/dev/null 2>&1)
  run_in "$@" -- "$BIN" daemon stop >/dev/null 2>&1
}

echo "== A: no gh installed, named token in the OS secure store"
if [ "$OS" = Darwin ]; then
  bash "$0" __scenario_a > "$TMP/a.log" 2>&1   # login Keychain
elif command -v dbus-run-session >/dev/null && { command -v gnome-keyring-daemon >/dev/null || [ -n "${KEYRING_ROOT:-}" ]; }; then
  KR=${KEYRING_ROOT:-}
  mkdir -p "$TMP/a-runtime"; chmod 700 "$TMP/a-runtime"
  env HOME="$TMP/a-home" XDG_RUNTIME_DIR="$TMP/a-runtime" \
    PATH="${KR:+$KR/usr/bin:}$PATH" LD_LIBRARY_PATH="${KR:+$KR/usr/lib/$(uname -m)-linux-gnu}" \
    NOGH="${KR:+$KR/usr/bin:}$NOGH" \
    dbus-run-session -- bash "$0" __scenario_a > "$TMP/a.log" 2>&1
else
  echo "SKIP  A: no Secret Service available (install gnome-keyring or set KEYRING_ROOT)"
fi
if [ -f "$TMP/a.log" ]; then
  grep -v '^A_RESULT' "$TMP/a.log"
  read -r _ AP AF A_T < <(grep '^A_RESULT' "$TMP/a.log" || echo "A_RESULT 0 1")
  PASSES=$((PASSES+AP)) FAILS=$((FAILS+AF))
fi

echo "== B: CONSONA_TOKEN env only (no gh, no secure store)"
read -r rc B_T < <(install_in b "$NOGH" CONSONA_TOKEN="$TOK")
check "B: install with CONSONA_TOKEN only [${B_T}s] rc=$rc" install_ok $rc b
run_in b "$NOGH" -- "$BIN" logs --event credential.resolve > "$TMP/b-logs.out" 2>&1
check "B: log says env CONSONA_TOKEN was used" grep -q 'env CONSONA_TOKEN' "$TMP/b-logs.out"
check "B: setup output has no '@@'" bash -c "! grep -q '@@' '$TMP/b-setup.out'"
finish b "$NOGH" CONSONA_TOKEN="$TOK"

echo "== C: gh login only"
read -r rc C_T < <(install_in c "$PATH" GH_CONFIG_DIR="$E2E_GH_CONFIG_DIR")
check "C: install with only the gh login [${C_T}s] rc=$rc" install_ok $rc c
run_in c "$PATH" -- "$BIN" logs --event credential.resolve > "$TMP/c-logs.out" 2>&1
check "C: log says gh login was used" grep -q 'gh login' "$TMP/c-logs.out"
finish c "$PATH" GH_CONFIG_DIR="$E2E_GH_CONFIG_DIR"

echo "== D: mixed: rejected $OWNER_ENV falls through to the gh login"
read -r rc D_T < <(install_in d "$PATH" GH_CONFIG_DIR="$E2E_GH_CONFIG_DIR" "$OWNER_ENV=$GARBAGE")
check "D: install succeeds despite a bad per-owner token [${D_T}s] rc=$rc" install_ok $rc d
run_in d "$PATH" -- "$BIN" logs --event credential > "$TMP/d-logs.out" 2>&1; show "$TMP/d-logs.out"
check "D: log: $OWNER_ENV failed with 401, then gh login used" bash -c "grep -q 'credential.fail.*$OWNER_ENV' '$TMP/d-logs.out' && grep -q 'status=401' '$TMP/d-logs.out' && grep -q 'credential.resolve.*gh login.*after 1 failed' '$TMP/d-logs.out'"
finish d "$PATH" GH_CONFIG_DIR="$E2E_GH_CONFIG_DIR" "$OWNER_ENV=$GARBAGE"

echo "== E: every credential rejected: one error listing each source"
read -r rc E_T < <(install_in e "$NOGH" CONSONA_TOKEN="$GARBAGE" "$OWNER_ENV=${GARBAGE%????}beef")
show "$TMP/e-install.out" | grep -v 'install-scripts' | head -40
check "E: install fails (rc=$rc)" test "$rc" -ne 0
check "E: consona's error lists both env tokens with 401 + fix URL, and gh as not installed" bash -c "
  grep -q '$OWNER_ENV (ghp_…beef): 401 bad credentials' '$TMP/e-install.out' &&
  grep -q 'env CONSONA_TOKEN (ghp_…' '$TMP/e-install.out' &&
  grep -q 'https://github.com/settings/personal-access-tokens' '$TMP/e-install.out' &&
  grep -q 'gh login: gh is not installed' '$TMP/e-install.out'"
check "E: npm's own error carries the same explanation" grep -qE 'npm (ERR!|error).*(401 bad credentials|any available GitHub credential)' "$TMP/e-install.out"
run_in e "$NOGH" -- "$BIN" logs --errors > "$TMP/e-logs.out" 2>&1
check "E: daemon log store has the error (error code unauthorized)" grep -q 'unauthorized' "$TMP/e-logs.out"
check "E: daemon.log has the same detailed message" grep -q '401 bad credentials' "$TMP/e-consona/daemon.log"
finish e "$NOGH" CONSONA_TOKEN="$GARBAGE"

echo "== F: no secure store: token add fails clearly, nothing written"
printf '%s' "$GARBAGE" | run_in f "$NOGH" -- "$BIN" token add x --no-verify > "$TMP/f-add.out" 2>&1; rc=$?
show "$TMP/f-add.out"
check "F: token add without a Secret Service fails (rc=$rc) pointing at env vars" bash -c "[ $rc -ne 0 ] && grep -q 'CONSONA_TOKEN' '$TMP/f-add.out' && grep -qi 'plaintext' '$TMP/f-add.out'"
check "F: no index file and no token anywhere under its HOME" bash -c "[ ! -e '$TMP/f-home/.config/consona/tokens.json' ] && ! grep -rqF -- '$GARBAGE' '$TMP/f-home'"

echo "== leaks: the real token is in no file written by any scenario"
LEAKS=$(grep -rlF -f <(printf '%s\n' "$TOK") "$TMP" 2>/dev/null | grep -v "^$TMP/bin/" | wc -l)
check "no token value in any HOME, consona home, log store, npmrc, lockfile or output" test "$LEAKS" -eq 0
unset TOK

echo "== timing: install with named token ${A_T:-n/a}s, CONSONA_TOKEN ${B_T}s, gh ${C_T}s, mixed (1 fallthrough) ${D_T}s"
echo "creds e2e: $PASSES passed, $FAILS failed"
if [ "$FAILS" -eq 0 ]; then echo "CREDS E2E PASS"; else KEEP=1; echo "CREDS E2E FAIL; kept $TMP"; exit 1; fi
