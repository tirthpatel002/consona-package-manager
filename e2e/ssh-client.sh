#!/usr/bin/env bash
# SSH remote end-to-end test, run on the client machine. The remote must
# have run `KEEP_REMOTE=1 e2e/local.sh` (its registered @e2euser/js-lib and
# ts-lib, and the remote-serve wrapper it leaves behind).
#   REMOTE_HOST=1.2.3.4 [REMOTE_USER=ubuntu] [REMOTE_IDENTITY=~/.ssh/key] e2e/ssh-client.sh
# Isolated client config; GitHub off (empty GH_CONFIG_DIR); the ssh config is
# a temp file passed with CONSONA_SSH_CONFIG, ~/.ssh is not modified.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
B=${E2E_DIR:-/tmp/consona-ssh-client-e2e}; rm -rf "$B"; mkdir -p "$B/gh-empty"
BIN=$B/consona
VM=${REMOTE_HOST:?set REMOTE_HOST} RU=${REMOTE_USER:-ubuntu} RS=${REMOTE_SERVE:-/tmp/consona-local-e2e/remote-serve}
IDF=() IDLINE=; if [ -n "${REMOTE_IDENTITY:-}" ]; then IDF=(--identity "$REMOTE_IDENTITY"); IDLINE="  IdentityFile $REMOTE_IDENTITY
  IdentitiesOnly yes"; fi
(cd "$ROOT" && go build -o "$BIN" ./cmd/consona) || { echo build failed; exit 1; }
export CONSONA_HOME=$B/home CONSONA_CONFIG_DIR=$B/cfg npm_config_userconfig=$B/npmrc CONSONA_PORT=${CONSONA_PORT:-48761}
export CONSONA_SSH_CONFIG=$B/ssh_config GH_CONFIG_DIR=$B/gh-empty CONSONA_REMOTE_CMD=$RS
unset GH_TOKEN GITHUB_TOKEN CONSONA_TOKEN; for v in $(env | sed -n 's/^\(CONSONA_TOKEN_[A-Z0-9_]*\)=.*/\1/p'); do unset "$v"; done   # GitHub off
export npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
cat > "$CONSONA_SSH_CONFIG" <<C
Host consona-remote
  HostName $VM
  User $RU
$IDLINE
Host github.com
  User git
Host *
  ServerAliveInterval 30
C
FAILS=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS+1)); }
check() { local m=$1; shift; if "$@"; then pass "$m"; else fail "$m"; fi; }
if command -v perl >/dev/null 2>&1; then now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }
else now() { python3 -c 'import time; print("%.3f" % time.time())'; }; fi
secs() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", b-a}'; }
show() { sed 's/^/      | /' "$1"; }
cleanup() {
  "$BIN" setup --undo >/dev/null 2>&1; "$BIN" daemon stop >/dev/null 2>&1
  for s in "$B"/home/ssh/*; do [ -S "$s" ] && ssh -o ControlPath="$s" -O exit dummy >/dev/null 2>&1; done
  rm -rf "$B"
}
trap cleanup EXIT

"$BIN" init --username boxuser > "$B/init.out" 2>&1; show "$B/init.out"
"$BIN" setup --no-autostart > "$B/setup.out" 2>&1; rc=$?; show "$B/setup.out"
check "setup without any GitHub login works for a local/ssh-only device (rc=$rc)" bash -c "[ $rc -eq 0 ] && grep -q 'GitHub not available' '$B/setup.out'"
"$BIN" remote list > "$B/list0.out" 2>&1; show "$B/list0.out"
check "ssh config host auto-detected (wildcards and git forges skipped)" bash -c "grep -q '^consona-remote .*ssh-config' '$B/list0.out' && ! grep -q github '$B/list0.out'"
t0=$(now); "$BIN" daemon start > /dev/null 2>&1
for _ in $(seq 100); do "$BIN" remote list 2>/dev/null | grep -q 'ok: @e2euser' && break; sleep 0.1; done; t1=$(now)
"$BIN" remote list > "$B/list1.out" 2>&1; show "$B/list1.out"
check "daemon learned the VM's packages in the background [$(secs $t0 $t1)s after start]" grep -q 'ok: @e2euser .*@e2euser/js-lib' "$B/list1.out"
mkdir -p "$B/app"; echo '{"name":"box-app","private":true,"version":"0.0.0"}' > "$B/app/package.json"
t0=$(now); (cd "$B/app" && "$BIN" install @e2euser/js-lib @e2euser/ts-lib > "$B/inst.out" 2>&1); rc=$?; t1=$(now); show "$B/inst.out"
OUT=$(cd "$B/app" && node -e 'console.log(require("@e2euser/js-lib").greet("box"), require("@e2euser/ts-lib").shout("ssh"))')
check "remote-only packages installed over ssh rc=$rc [$(secs $t0 $t1)s]: $OUT" bash -c "[ $rc -eq 0 ] && [ '$OUT' = 'hello box from js-lib v1 SSH!' ] && grep -q 'js-lib@1.0.0-local.*<- ssh consona-remote (from ssh config) \[device ' '$B/inst.out'"
I=$(node -p 'require(process.argv[1]).packages["node_modules/@e2euser/js-lib"].integrity' "$B/app/package-lock.json")
REMOTE_I=$(ssh -F "$CONSONA_SSH_CONFIG" -o BatchMode=yes consona-remote "cat $(dirname "$RS")/remote-home/cache/npm/e2euser/js-lib/*/*/artifact.json" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{for(const l of s.split(/(?<=})\s*(?={)/)){try{console.log("sha512-"+JSON.parse(l).sha512)}catch{}}})')
check "end-to-end sha512: client lockfile integrity = the remote's built tarball" bash -c "grep -qxF -- '$I' <<< '$REMOTE_I'"
rm -rf "$B/app/node_modules"; t0=$(now); (cd "$B/app" && "$BIN" install > "$B/inst2.out" 2>&1); rc=$?; t1=$(now)
check "reinstall from lockfile rc=$rc [$(secs $t0 $t1)s]" test $rc -eq 0
"$BIN" remote add vm $RU@$VM "${IDF[@]}" --remote-cmd $RS > "$B/add.out" 2>&1; rc=$?; show "$B/add.out"
check "remote add (explicit, tested on add) rc=$rc" bash -c "[ $rc -eq 0 ] && grep -q 'ok: @e2euser' '$B/add.out'"
"$BIN" which @e2euser/ts-lib > "$B/which.out" 2>&1; show "$B/which.out"
check "explicit remote is asked before the auto-detected one" grep -q '<- ssh vm ' "$B/which.out"
"$BIN" remote add nocon $RU@$VM "${IDF[@]}" --remote-cmd /nonexistent/consona > "$B/nocon.out" 2>&1; rc=$?; show "$B/nocon.out"
check "remote without consona: clear error" bash -c "[ $rc -ne 0 ] && grep -q 'consona is not installed on the remote' '$B/nocon.out'"
"$BIN" remote add bad ubuntu@192.0.2.1 --no-test > /dev/null 2>&1
"$BIN" remote list > "$B/list2.out" 2>&1; show "$B/list2.out"
t0=$(now); "$BIN" which @e2euser/nope > "$B/nope.out" 2>&1; t1=$(now); show "$B/nope.out"
P1=$(secs $t0 $t1)
t0=$(now); "$BIN" which @e2euser/nope2 > /dev/null 2>&1; t1=$(now); P2=$(secs $t0 $t1)
echo "      | unknown name with 2 live remotes + 1 unreachable + 1 without consona: ${P1}s first, ${P2}s after (lists cached)"
check "unknown name: clear not-found, cached lists make the 2nd lookup fast" bash -c "grep -q 'not found' '$B/nope.out' && awk 'BEGIN{exit !($P2 < 1.0)}'"
"$BIN" logs --event source.resolve --json > "$B/log-resolve.json" 2>&1
"$BIN" logs --event ssh.probe --errors --json > "$B/log-probe.json" 2>&1
"$BIN" logs --event remote --json > "$B/log-remote.json" 2>&1
check "logs: ssh resolutions recorded (source.resolve, fields.source=ssh)" grep -q '"source":"ssh"' "$B/log-resolve.json"
check "logs: ssh probe failures recorded with ssh's reason (unreachable remote, remote without consona)" bash -c "grep -q '192.0.2.1' '$B/log-probe.json' && grep -q 'no-consona' '$B/log-probe.json'"
check "logs: not-found names list what was tried (source.fail)" bash -c "'$BIN' logs --event source.fail --json | grep -q 'e2euser/nope.*tried'"
"$BIN" remote remove nocon >/dev/null; "$BIN" remote remove bad >/dev/null; "$BIN" remote remove vm > "$B/rm.out"; show "$B/rm.out"
check "remote remove" bash -c "! '$BIN' remote list | grep -q '^vm '"
"$BIN" logs --event remote --json > "$B/log-remote.json" 2>&1
check "logs: remote add/test/remove recorded" bash -c "grep -q '\"event\":\"remote.add\"' '$B/log-remote.json' && grep -q '\"event\":\"remote.test\"' '$B/log-remote.json' && grep -q '\"event\":\"remote.remove\"' '$B/log-remote.json'"
check "no token-shaped string in the client's logs or outputs" bash -c "! grep -rqE 'gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}' '$B/cfg' '$B'/*.out '$B'/*.json"
echo "== $FAILS failure(s)"
exit $FAILS
