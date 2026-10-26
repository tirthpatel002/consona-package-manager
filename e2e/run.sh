#!/usr/bin/env bash
# End-to-end test against real private GitHub repos (see e2e/fixtures.sh).
# Needs: go, node/npm, gh logged in. Uses a temp npmrc and consona home, so
# the user's ~/.npmrc and cache are untouched (npm's own cache is shared,
# which keeps the plain-npm vs consona timing comparison fair: both warm).
set -uo pipefail
OWNER=${OWNER:-rakesh-jain-cb1998}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d /tmp/consona-e2e.XXXXXX)
export CONSONA_HOME=$TMP/consona-home CONSONA_PORT=${CONSONA_PORT:-48731}
export npm_config_userconfig=$TMP/npmrc npm_config_update_notifier=false npm_config_fund=false
BIN=$TMP/bin/consona
FAILS=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS+1)); }
check() { local msg=$1; shift; if "$@"; then pass "$msg"; else fail "$msg"; fi; }
now() { date +%s.%N; }
secs() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", b-a}'; }

SVC=consona; [ "$CONSONA_PORT" = 48731 ] || SVC=consona-$CONSONA_PORT
UNIT=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/$SVC.service
# Ports are overridable so several runs (or a developer daemon) can coexist.
PORT2=${E2E_PORT2:-48732} FPORT=${E2E_FPORT:-48799} FRESH_PORT=${E2E_FRESH_PORT:-48731} FAKE=; KEEP=
in2() { CONSONA_PORT=$PORT2 CONSONA_HOME=$TMP/h2 npm_config_userconfig=$TMP/authrc "$@"; }   # 2nd instance, token-requiring upstream
cleanup() {   # never leave a service, daemon or fake registry behind
  [ -x "$BIN" ] && { "$BIN" setup --undo >/dev/null 2>&1; in2 "$BIN" daemon stop >/dev/null 2>&1; }
  [ -n "$FAKE" ] && kill "$FAKE" 2>/dev/null
  [ -n "${HTTP:-}" ] && kill "$HTTP" "${HTTP2:-}" 2>/dev/null
  [ -x "$BIN" ] && env -i HOME="$TMP/fresh-home" PATH="$PATH" CONSONA_PORT="$FRESH_PORT" "$BIN" daemon stop >/dev/null 2>&1
  if [ -z "$KEEP" ]; then rm -rf "$TMP"; fi
}
trap cleanup EXIT
(cd "$ROOT" && go build -o "$BIN" ./cmd/consona && go build -o "$TMP/bin/fakeregistry" ./e2e/fakeregistry) || { echo "build failed"; KEEP=1; exit 1; }
# A pre-existing user npmrc: setup must override its registry, undo restore it.
printf 'registry=https://registry.npmjs.org/\nfund=false\n' > "$TMP/npmrc"; cp "$TMP/npmrc" "$TMP/npmrc.orig"
: > "$TMP/plain.npmrc"
npmplain() { npm_config_userconfig=$TMP/plain.npmrc npm "$@"; }   # plain npm, no consona at all
"$BIN" setup > "$TMP/setup.out" || { echo "consona setup failed"; exit 1; }
sed 's/^/      | /' "$TMP/setup.out"
check "setup points npm's registry at the daemon" test "$(npm config get registry)" = "http://127.0.0.1:$CONSONA_PORT/npm/"
check "setup registered auto-start: systemd user unit $SVC enabled + active" \
  bash -c "systemctl --user is-enabled -q $SVC && systemctl --user is-active -q $SVC && grep -q '^Restart=always' '$UNIT'"
check "npmrc has no secret (only the placeholder login + fail-fast retry settings)" \
  bash -c "grep -q '^//127.0.0.1:$CONSONA_PORT/npm/:_authToken=consona-uses-your-registry-login$' '$TMP/npmrc' && grep -q '^fetch-retry-maxtimeout=2000$' '$TMP/npmrc'"

PUBLIC_DEPS='"react": "^18.3.1", "react-dom": "^18.3.1", "react-router-dom": "^6.26.0",
    "clsx": "^2.1.1", "zod": "^3.23.8"'
PUBLIC_DEV='"vite": "^5.4.0", "@vitejs/plugin-react": "^4.3.1", "typescript": "^5.5.4",
    "@types/react": "^18.3.3", "@types/react-dom": "^18.3.0"'

mkdir -p "$TMP/app/src" "$TMP/plain"
cat > "$TMP/app/package.json" <<J
{ "name": "e2e-app", "private": true, "version": "0.0.0", "type": "module",
  "dependencies": { "@$OWNER/consona-e2e-lib-a": "^1.0.0", "@$OWNER/consona-e2e-lib-c": "*", "@$OWNER/consona-e2e-lib-ts": "^1.0.0", $PUBLIC_DEPS },
  "devDependencies": { $PUBLIC_DEV } }
J
cat > "$TMP/plain/package.json" <<J
{ "name": "e2e-plain", "private": true, "version": "0.0.0", "type": "module",
  "dependencies": { $PUBLIC_DEPS }, "devDependencies": { $PUBLIC_DEV } }
J
cat > "$TMP/app/src/App.tsx" <<'TSX'
import { useMemo, useState } from "react";
import clsx from "clsx";
import { z } from "zod";
import { greet } from "@OWNER/consona-e2e-lib-a";
import { formatPrice } from "@OWNER/consona-e2e-lib-c";
import { slugify } from "@OWNER/consona-e2e-lib-ts";

const Item = z.object({ id: z.number(), name: z.string(), price: z.number() });
type Item = z.infer<typeof Item>;

const ITEMS: Item[] = [
  { id: 1, name: "Keyboard", price: 49.5 },
  { id: 2, name: "Mouse", price: 19 },
  { id: 3, name: "Monitor", price: 189.99 },
].map((i) => Item.parse(i));

function Row({ item, selected, onToggle }: { item: Item; selected: boolean; onToggle: () => void }) {
  return (
    <li id={slugify(item.name)} className={clsx("row", { selected })} onClick={onToggle}>
      <span>{item.name}</span>
      <span>{formatPrice(item.price)}</span>
    </li>
  );
}

export default function App() {
  const [name, setName] = useState("consona");
  const [picked, setPicked] = useState<number[]>([]);
  const total = useMemo(
    () => ITEMS.filter((i) => picked.includes(i.id)).reduce((s, i) => s + i.price, 0),
    [picked],
  );
  const toggle = (id: number) =>
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));

  return (
    <main>
      <h1>{greet(name)}</h1>
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="your name" />
      <ul>
        {ITEMS.map((item) => (
          <Row key={item.id} item={item} selected={picked.includes(item.id)} onToggle={() => toggle(item.id)} />
        ))}
      </ul>
      <p>
        {picked.length} selected, total {formatPrice(total)}
      </p>
      <footer>Private packages served by consona, public ones by npm.</footer>
    </main>
  );
}
TSX
sed -i "s/@OWNER/@$OWNER/g" "$TMP/app/src/App.tsx"

reset() { rm -rf "$1/node_modules" "$1/package-lock.json"; }

echo "== first installs (consona cache cold: private packages fetched + built)"
t0=$(now); (cd "$TMP/plain" && npmplain install >/dev/null 2>&1); rc=$?; t1=$(now)
NPM_COLD=$(secs $t0 $t1); check "plain npm first install exit 0 [${NPM_COLD}s]" test $rc -eq 0
t0=$(now); (cd "$TMP/app" && "$BIN" install > "$TMP/first.out" 2>&1); rc=$?; t1=$(now)
CON_COLD=$(secs $t0 $t1); check "consona first install (cold consona cache) exit 0 [${CON_COLD}s]" test $rc -eq 0

echo "== timed runs (warm caches, no lockfile/node_modules, daemon restarted for consona, audit on)"
reset "$TMP/plain"; t0=$(now); (cd "$TMP/plain" && npmplain install > "$TMP/plain.out" 2>&1); rc=$?; t1=$(now)
NPM_T=$(secs $t0 $t1); check "plain npm install exit 0 [${NPM_T}s]" test $rc -eq 0
reset "$TMP/app"; "$BIN" daemon stop >/dev/null
t0=$(now); (cd "$TMP/app" && "$BIN" install > "$TMP/install.out" 2>&1); rc=$?; t1=$(now)
CON_T=$(secs $t0 $t1); check "consona install exit 0 [${CON_T}s]" test $rc -eq 0
grep -v install-scripts "$TMP/install.out" | sed 's/^/      | /'

NM=$TMP/app/node_modules/@$OWNER
for p in a b c ts; do check "private lib-$p installed" test -f "$NM/consona-e2e-lib-$p/package.json"; done
check "lib-a respects \"files\" (notes.txt not packed)" test ! -e "$NM/consona-e2e-lib-a/notes.txt"
check "lib-c (no tags) served from default branch as 0.0.0-<sha>" \
  grep -Eq '"version": ?"0\.0\.0-[0-9a-f]{7}"' "$NM/consona-e2e-lib-c/package.json"
OUT=$(cd "$TMP/app" && node -e "console.log(require('@$OWNER/consona-e2e-lib-a').greet('e2e'))" 2>&1)
check "private-of-private works (lib-a -> lib-b): $OUT" test "$OUT" = "HELLO E2E!"
TS=$NM/consona-e2e-lib-ts
check "lib-ts built by its prepare (dist/ packed, src/ not)" test -f "$TS/dist/index.js" -a -f "$TS/dist/index.d.ts" -a ! -e "$TS/src"
OUT=$(cd "$TMP/app" && node -e "console.log(require('@$OWNER/consona-e2e-lib-ts').slugify('  Hello Consona World! '))" 2>&1)
check "compiled TypeScript works at runtime: $OUT" test "$OUT" = "hello-consona-world"
for p in react react-dom react-router-dom clsx zod vite @vitejs/plugin-react typescript @types/react; do
  [ -f "$TMP/app/node_modules/$p/package.json" ] || fail "public $p missing"
done
pass "public packages present (react, vite, typescript, ...)"

read -r PRIV PUB OTHER < <(node -e '
const l=require(process.argv[1]).packages;let a=0,b=0,c=0;
for(const [k,v] of Object.entries(l)){if(!k)continue;const r=v.resolved||"";
 if(r.startsWith("http://127.0.0.1:"))a++;else if(r.startsWith("https://registry.npmjs.org/"))b++;else c++}
console.log(a,b,c)' "$TMP/app/package-lock.json")
check "lockfile: $PRIV private via consona, $PUB public via registry.npmjs.org, $OTHER other" test "$PRIV" -eq 4 -a "$OTHER" -eq 0

check "npm's own install summary (audit ran during install)" grep -qE "added [0-9]+ packages, and audited [0-9]+ packages in" "$TMP/install.out"

LOG=$CONSONA_HOME/daemon.log
pubstats() { awk -v o="/npm/@$OWNER/" '$3=="GET"||$3=="HEAD"{ if (index($4,o)==1) next; if ($5=="302") r++; else b++ } END{printf "%d %d", r, b}' "$LOG"; }
read -r REDIR BODIES < <(pubstats)
check "daemon served 0 public package bodies ($REDIR public requests answered by 302 redirect)" test "$BODIES" -eq 0 -a "$REDIR" -gt 0

echo "== audit through the daemon: private names withheld, results genuine"
WH=$(grep -o 'audit: forwarding [0-9]* public package names, withheld [0-9]* private' "$LOG" | tail -1)
check "install-time audit filtered by daemon: $WH" bash -c "echo '$WH' | grep -q 'withheld 4 private'"
(cd "$TMP/app" && "$BIN" install --loglevel http > "$TMP/http.out" 2>&1)
(cd "$TMP/app" && npm audit --loglevel http > "$TMP/audit-http.out" 2>&1)
check "plain npm audit + consona install: no audit request and no private name to registry.npmjs.org" \
  bash -c "! grep -hE 'registry\.npmjs\.org/(-/npm/v1/security|@$OWNER)' '$TMP/http.out' '$TMP/audit-http.out' | grep -q ."
check "plain npm audit posted to the daemon" grep -q "POST 200 http://127.0.0.1:$CONSONA_PORT/npm/-/npm/v1/security/advisories/bulk" "$TMP/audit-http.out"
vulns() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{console.log(JSON.stringify(JSON.parse(s).metadata.vulnerabilities))})'; }
CA=$(cd "$TMP/app" && npm audit --json 2>/dev/null | vulns)
PA=$(cd "$TMP/plain" && npmplain audit --json 2>/dev/null | vulns)
check "plain npm audit in app = plain-npm audit without consona ($CA)" test -n "$CA" -a "$CA" = "$PA"

echo "== crash: systemd restarts the daemon"
PID1=$(systemctl --user show -p MainPID --value $SVC); kill -9 "$PID1"
for _ in $(seq 50); do PID2=$(systemctl --user show -p MainPID --value $SVC); [ "$PID2" != 0 ] && [ "$PID2" != "$PID1" ] && curl -sf "http://127.0.0.1:$CONSONA_PORT/-/consona/ping" >/dev/null && break; sleep 0.1; done
check "killed daemon (kill -9 pid $PID1) restarted by systemd as pid $PID2" bash -c "[ '$PID2' != 0 ] && [ '$PID2' != '$PID1' ] && systemctl --user is-active -q $SVC"

echo "== plain npm (not via consona) with the (restarted) daemon running"
reset "$TMP/app"; t0=$(now); (cd "$TMP/app" && npm install --loglevel http > "$TMP/plain-in-app.out" 2>&1); rc=$?; t1=$(now)
check "plain npm install in the app exit 0 [$(secs $t0 $t1)s]" test $rc -eq 0
check "plain npm install got all 4 private packages" bash -c "ls -d '$NM'/consona-e2e-lib-{a,b,c,ts} >/dev/null"
check "plain npm install: no private name to registry.npmjs.org" \
  bash -c "! grep -E 'registry\.npmjs\.org/(-/npm/v1/security|@$OWNER)' '$TMP/plain-in-app.out' | grep -q ."
read -r P2 B2 O2 < <(node -e '
const l=require(process.argv[1]).packages;let a=0,b=0,c=0;
for(const [k,v] of Object.entries(l)){if(!k)continue;const r=v.resolved||"";
 if(r.startsWith("http://127.0.0.1:"))a++;else if(r.startsWith("https://registry.npmjs.org/"))b++;else c++}
console.log(a,b,c)' "$TMP/app/package-lock.json")
check "lockfile after plain npm: public resolved = registry.npmjs.org ($B2), private via consona ($P2), other $O2" test "$P2" -eq 4 -a "$O2" -eq 0 -a "$B2" -eq "$PUB"
read -r REDIR BODIES < <(pubstats)
check "still 0 public bodies served by the daemon ($REDIR redirects)" test "$BODIES" -eq 0

TOKEN_FILE=$(mktemp); chmod 600 "$TOKEN_FILE"
TOK=$(gh auth token 2>/dev/null) || TOK=$(gh config get -h github.com oauth_token)
[ ${#TOK} -ge 20 ] || fail "could not read gh token for leak check"
printf '%s\n' "$TOK" > "$TOKEN_FILE"; unset TOK
LEAKS=$(grep -rlFf "$TOKEN_FILE" "$TMP" 2>/dev/null | wc -l); rm -f "$TOKEN_FILE"
check "gh token in no written file (lockfile, npmrc, cache, logs, node_modules)" test "$LEAKS" -eq 0

echo "== negative: nonexistent private name"
cp "$TMP/app/package.json" "$TMP/pkg.before"
(cd "$TMP/app" && "$BIN" install "@$OWNER/consona-e2e-does-not-exist" --loglevel http > "$TMP/neg.out" 2>&1); rc=$?
check "nonexistent private name exits non-zero (rc=$rc)" test $rc -ne 0
check "clear consona message" grep -q "^consona: @$OWNER/consona-e2e-does-not-exist is private" "$TMP/neg.out"
grep "^consona:" "$TMP/neg.out" | sed 's/^/      | /'
check "no registry.npmjs.org request for the private name" \
  bash -c "! grep -i 'registry.npmjs.org' '$TMP/neg.out' | grep -q 'does-not-exist'"
check "package.json unchanged" cmp -s "$TMP/pkg.before" "$TMP/app/package.json"

echo "== daemon down: npm fails closed and fast (default retries, no extra flags)"
"$BIN" daemon stop >/dev/null
t0=$(now); (cd "$TMP/app" && npm view "@$OWNER/consona-e2e-never-fetched" version --loglevel http > "$TMP/down.out" 2>&1); rc=$?; t1=$(now)
DOWN=$(secs $t0 $t1)
check "plain npm with daemon down exits non-zero (rc=$rc) in ${DOWN}s (< 6s)" bash -c "[ $rc -ne 0 ] && awk -v d=$DOWN 'BEGIN{exit !(d<6)}'"
check "error names the daemon address (ECONNREFUSED 127.0.0.1:$CONSONA_PORT)" grep -q "ECONNREFUSED 127.0.0.1:$CONSONA_PORT" "$TMP/down.out"
check "no request to registry.npmjs.org while the daemon is down" bash -c "! grep -q 'registry.npmjs.org' '$TMP/down.out'"
grep -m1 "ECONNREFUSED" "$TMP/down.out" | sed 's/^/      | /'

echo "== previous registry needs a token: auth lookup like npm, public writes, OTP, login/logout via consona"
rnd() { echo "fake$1-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"; }
FAKE_TOKEN=$(rnd 1) FAKE_TOKEN2=$(rnd 2) FAKE_TOKEN3=$(rnd 3); export FAKE_TOKEN FAKE_TOKEN2 FAKE_TOKEN3
FLOG=$TMP/fake.log FURL=http://127.0.0.1:$FPORT NERF=//127.0.0.1:$FPORT/
"$TMP/bin/fakeregistry" -addr 127.0.0.1:$FPORT -log "$FLOG" 2>"$TMP/fake.err" & FAKE=$!
for _ in $(seq 50); do curl -s "$FURL/" >/dev/null && break; sleep 0.1; done
T2=$FAKE_TOKEN2 T3=$FAKE_TOKEN3; ALLTOKS="$FAKE_TOKEN $FAKE_TOKEN2 $FAKE_TOKEN3"
printf 'registry=%s/\n%s:_authToken=%s\n' "$FURL" "$NERF" "$FAKE_TOKEN" > "$TMP/authrc"
unset FAKE_TOKEN FAKE_TOKEN2 FAKE_TOKEN3   # the daemon never sees them: only npm config / npm's env has them
ss -ltnp 2>/dev/null | grep ":$PORT2 " >> "$TMP/port2-before.txt"
in2 "$BIN" setup --no-autostart > "$TMP/setup2.out" 2>&1 || { ss -ltnp 2>/dev/null | grep ":$PORT2 " >> "$TMP/setup2.out"; fail "setup of 2nd instance"; }
D2LOG=$TMP/h2/daemon.log
check "2nd instance: npm registry = consona, previous = token-requiring fake" test "$(in2 npm config get registry)" = "http://127.0.0.1:$PORT2/npm/"
W=$(in2 npm whoami 2>"$TMP/whoami.err"); check "auth from user npmrc: npm whoami through consona = $W" test "$W" = "e2e-user"
mkdir -p "$TMP/app2" && echo '{"name":"app2","private":true,"version":"0.0.0"}' > "$TMP/app2/package.json"
printf '%s:_authToken=${NPM_TOKEN}\n' "$NERF" > "$TMP/app2/.npmrc"
W=$(cd "$TMP/app2" && NPM_TOKEN=$T2 in2 npm whoami 2>/dev/null)
check "auth from project .npmrc with \${NPM_TOKEN} in npm's env (beats user npmrc): $W" test "$W" = "e2e-project-user"
W=$(cd "$TMP/app2" && NPM_TOKEN=$T2 in2 env "npm_config_${NERF}:_authToken=$T3" npm whoami 2>/dev/null)
check "auth from npm_config_//host/:_authToken env (beats project): $W" test "$W" = "e2e-env-user"
(cd "$TMP/app2" && NPM_TOKEN=$T2 in2 npm install "@acme/secret" "@$OWNER/consona-e2e-lib-c" --loglevel http > "$TMP/install2.out" 2>&1); rc=$?
check "install auth-only @acme/secret + private lib-c (rc=$rc)" bash -c "[ $rc -eq 0 ] && [ -f '$TMP/app2/node_modules/@acme/secret/index.js' ] && [ -f '$TMP/app2/node_modules/@$OWNER/consona-e2e-lib-c/package.json' ]"
check "fake got the auth-only packument via consona with the project login" grep -q 'GET /@acme/secret auth=true user=e2e-project-user' "$FLOG"
rm -f "$TMP/app2/.npmrc"   # the rest of the checks use the user npmrc login
check "install-time audit forwarded to the fake with auth, private names withheld" grep -q 'POST /-/npm/v1/security/advisories/bulk auth=true' "$FLOG"
mkdir -p "$TMP/pub" "$TMP/priv" "$TMP/otp1" "$TMP/otp2"
echo '{"name":"consona-e2e-public-pkg","version":"1.0.0"}' > "$TMP/pub/package.json"
echo '{"name":"otp-e2e-one","version":"1.0.0"}' > "$TMP/otp1/package.json"
echo '{"name":"otp-e2e-two","version":"1.0.0"}' > "$TMP/otp2/package.json"
echo "{\"name\":\"@$OWNER/consona-e2e-priv-publish\",\"version\":\"1.0.0\"}" > "$TMP/priv/package.json"
(cd "$TMP/pub" && in2 npm publish > "$TMP/publish.out" 2>&1); rc=$?
check "npm publish (public name, no flags) reaches the previous registry with auth (rc=$rc)" bash -c "[ $rc -eq 0 ] && grep -q 'PUT /consona-e2e-public-pkg auth=true' '$FLOG'"
(cd "$TMP/pub" && in2 npm dist-tag add consona-e2e-public-pkg@1.0.0 beta > "$TMP/disttag.out" 2>&1); rc=$?
check "npm dist-tag add via consona (rc=$rc)" bash -c "[ $rc -eq 0 ] && grep -q 'PUT /-/package/consona-e2e-public-pkg/dist-tags/beta auth=true' '$FLOG'"
(cd "$TMP/otp1" && in2 npm publish --otp=123456 > "$TMP/otp1.out" 2>&1); rc=$?
check "2FA: npm publish --otp=... passes the OTP through (rc=$rc)" bash -c "[ $rc -eq 0 ] && grep -q 'PUT /otp-e2e-one auth=true user=e2e-user otp=true' '$FLOG'"
(cd "$TMP/otp2" && (sleep 4; echo 123456; sleep 3) | in2 timeout 40 script -qec "npm publish" /dev/null > "$TMP/otp2.out" 2>&1); rc=$?
check "2FA: interactive OTP challenge (401 + prompt, retry with OTP) through consona (rc=$rc)" \
  bash -c "[ $rc -eq 0 ] && grep -q 'Enter OTP' '$TMP/otp2.out' && grep -q 'PUT /otp-e2e-two auth=true user=e2e-user otp=false' '$FLOG' && grep -q 'PUT /otp-e2e-two auth=true user=e2e-user otp=true' '$FLOG'"
(cd "$TMP/priv" && in2 npm publish > "$TMP/privpub.out" 2>&1); rc=$?
check "private-scope publish refused with 403 (rc=$rc)" bash -c "[ $rc -ne 0 ] && grep -q 403 '$TMP/privpub.out'"
in2 npm logout > "$TMP/logout.out" 2>&1; rc=$?
for _ in $(seq 30); do grep -q _authToken "$TMP/authrc" || break; sleep 0.1; done
check "npm logout through consona revokes the user's upstream token and removes it from the npmrc (rc=$rc)" \
  bash -c "[ $rc -eq 0 ] && grep -q 'DELETE /-/user/token/\[redacted\] auth=true user=e2e-user' '$FLOG' && ! grep -q '_authToken' '$TMP/authrc'"
check "after logout: private routing still works" bash -c "cd '$TMP/app2' && CONSONA_PORT=$PORT2 CONSONA_HOME='$TMP/h2' npm_config_userconfig='$TMP/authrc' npm view '@$OWNER/consona-e2e-lib-c' name >/dev/null 2>&1"
in2 npm login --auth-type=web < /dev/null > "$TMP/login.out" 2>&1; rc=$?
W=$(in2 npm whoami 2>/dev/null)
check "after logout: npm login (web) works again without re-running setup; whoami = $W (rc=$rc)" bash -c "[ $rc -eq 0 ] && [ '$W' = e2e-web-user ]"
check "npm rewrote the npmrc (comments gone) and consona still routes it" bash -c "! grep -q '^#' '$TMP/authrc' && cd '$TMP/app2' && CONSONA_PORT=$PORT2 CONSONA_HOME='$TMP/h2' npm_config_userconfig='$TMP/authrc' npm view '@$OWNER/consona-e2e-lib-c' name >/dev/null 2>&1"
check "the fake (previous registry) never saw a private name" bash -c "! grep -qi '@$OWNER' '$FLOG'"
LEAK=0; for t in $ALLTOKS; do grep -qF "$t" "$D2LOG" "$LOG" && LEAK=1; done; grep -q 'fake-web-' "$D2LOG" "$LOG" && LEAK=1
check "no token in either daemon log (3 upstream tokens, web-login token); logout path redacted" bash -c "[ $LEAK -eq 0 ] && grep -q 'user/token/\[redacted\]' '$D2LOG'"
unset ALLTOKS T2 T3
in2 "$BIN" setup --undo >/dev/null 2>&1
check "2nd undo: registry = the fake again, no consona lines, web login moved to the fake's entry" \
  bash -c "[ \"\$(npm_config_userconfig='$TMP/authrc' npm config get registry)\" = '$FURL/' ] && ! grep -q 127.0.0.1:$PORT2 '$TMP/authrc' && [ \"\$(npm_config_userconfig='$TMP/authrc' npm whoami 2>/dev/null)\" = e2e-web-user ]"
kill "$FAKE" 2>/dev/null; FAKE=

echo "== undo"
"$BIN" setup --undo >/dev/null
check "setup --undo restores the previous npmrc exactly" cmp -s "$TMP/npmrc" "$TMP/npmrc.orig"
check "npm registry is registry.npmjs.org again" test "$(npm config get registry)" = "https://registry.npmjs.org/"
check "undo removed the auto-start unit and stopped the service" bash -c "[ ! -e '$UNIT' ] && ! systemctl --user is-active -q $SVC"

echo "== fresh machine: empty HOME, no npmrc, no npm cache, gh login only via GH_TOKEN in this process"
FH=$TMP/fresh-home; mkdir -p "$FH"
RC_BEFORE=$(sha256sum "$HOME/.npmrc" 2>/dev/null)
TOKEN_FILE=$(mktemp); chmod 600 "$TOKEN_FILE"
TOK=$(gh auth token 2>/dev/null) || TOK=$(gh config get -h github.com oauth_token)
printf '%s\n' "$TOK" > "$TOKEN_FILE"
fresh() { env -i HOME="$FH" PATH="$PATH" USER="$USER" LANG=C.UTF-8 GH_TOKEN="$TOK" CONSONA_PORT="$FRESH_PORT" "$@"; }
cp "$TMP/app/package.json" "$FH/pkg.json"; mkdir -p "$FH/app/src"; cp "$TMP/app/package.json" "$FH/app/"; cp "$TMP/app/src/App.tsx" "$FH/app/src/"
(cd "$FH" && fresh "$BIN" setup --no-autostart > "$TMP/fresh-setup.out" 2>&1); rc=$?
check "fresh: consona setup (no prior npmrc) rc=$rc" bash -c "[ $rc -eq 0 ] && [ \"\$(cd '$FH' && env -i HOME='$FH' PATH='$PATH' npm config get registry)\" = http://127.0.0.1:$FRESH_PORT/npm/ ]"
(cd "$FH/app" && fresh "$BIN" install > "$TMP/fresh-install.out" 2>&1); rc=$?
check "fresh: consona install of the e2e app (empty npm cache) rc=$rc" bash -c "[ $rc -eq 0 ] && ls -d '$FH'/app/node_modules/@$OWNER/consona-e2e-lib-{a,b,c,ts} >/dev/null"
rm -rf "$FH/app/node_modules" "$FH/app/package-lock.json"
(cd "$FH/app" && fresh npm install > "$TMP/fresh-npm.out" 2>&1); rc=$?
check "fresh: plain npm install rc=$rc" bash -c "[ $rc -eq 0 ] && [ -f '$FH/app/node_modules/@$OWNER/consona-e2e-lib-ts/dist/index.js' ] && [ -f '$FH/app/node_modules/react/package.json' ]"
FA=$(cd "$FH/app" && fresh npm audit --json 2>/dev/null | vulns)
check "fresh: npm audit = plain-npm audit ($FA)" test -n "$FA" -a "$FA" = "$PA"
(cd "$FH" && fresh "$BIN" setup --undo > "$TMP/fresh-undo.out" 2>&1); rc=$?
check "fresh: undo rc=$rc; npmrc removed (it did not exist before), daemon stopped" \
  bash -c "[ $rc -eq 0 ] && [ ! -e '$FH/.npmrc' ] && ! curl -s http://127.0.0.1:$FRESH_PORT/-/consona/ping >/dev/null"
unset TOK
LEAKS=$(grep -rlFf "$TOKEN_FILE" "$FH" "$TMP"/*.out 2>/dev/null | wc -l); rm -f "$TOKEN_FILE"
check "fresh: gh token in no written file under the fresh HOME" test "$LEAKS" -eq 0
check "fresh: real ~/.npmrc untouched" test "$(sha256sum "$HOME/.npmrc" 2>/dev/null)" = "$RC_BEFORE"

echo "== release build + install.sh via a local http server"
VER=0.9.0-e2e DIST=$TMP/dist HPORT=${E2E_HPORT:-48790}
(cd "$ROOT" && bash scripts/release.sh "$VER" "$DIST" > "$TMP/release.out" 2>&1); rc=$?
check "release: 5 static binaries + SHA256SUMS (rc=$rc)" bash -c "[ $rc -eq 0 ] && [ \$(grep -c ' consona_${VER}_' '$DIST/SHA256SUMS') -eq 5 ] && (cd '$DIST' && sha256sum -c --quiet SHA256SUMS) && file '$DIST/consona_${VER}_linux_amd64' | grep -q 'statically linked'"
(cd "$DIST" && exec python3 -m http.server $HPORT --bind 127.0.0.1 > "$TMP/http.log" 2>&1) & HTTP=$!
for _ in $(seq 50); do curl -sf "http://127.0.0.1:$HPORT/SHA256SUMS" >/dev/null && break; sleep 0.1; done
IH=$TMP/install-home; mkdir -p "$IH"
curl -fsSL "http://127.0.0.1:$HPORT/install.sh" | env HOME="$IH" PATH="$PATH" CONSONA_BASE_URL="http://127.0.0.1:$HPORT" sh > "$TMP/installer.out" 2>&1; rc=$?
sed 's/^/      | /' "$TMP/installer.out"
check "install.sh: downloads, verifies, installs to ~/.local/bin; consona version = $VER (rc=$rc)" \
  bash -c "[ $rc -eq 0 ] && [ \"\$('$IH/.local/bin/consona' version)\" = 'consona $VER' ] && grep -q 'consona setup' '$TMP/installer.out'"
mkdir -p "$TMP/bad" && cp "$DIST"/* "$TMP/bad/" && for f in "$TMP/bad"/consona_${VER}_*; do printf x >> "$f"; done
IB=$TMP/install-bad; mkdir -p "$IB"
(cd "$TMP/bad" && exec python3 -m http.server $((HPORT+1)) --bind 127.0.0.1 >/dev/null 2>&1) & HTTP2=$!
for _ in $(seq 50); do curl -sf "http://127.0.0.1:$((HPORT+1))/SHA256SUMS" >/dev/null && break; sleep 0.1; done
sh "$DIST/install.sh" --base-url "http://127.0.0.1:$((HPORT+1))" --dir "$IB/bin" > "$TMP/installer-bad.out" 2>&1; rc=$?
check "install.sh: tampered binary rejected (checksum mismatch), nothing installed (rc=$rc)" \
  bash -c "[ $rc -ne 0 ] && grep -q 'checksum mismatch' '$TMP/installer-bad.out' && [ ! -e '$IB/bin/consona' ]"
kill $HTTP $HTTP2 2>/dev/null

echo "== timing: first install consona ${CON_COLD}s vs npm ${NPM_COLD}s; warm: consona ${CON_T}s vs plain npm ${NPM_T}s (overhead $(awk -v a=$CON_T -v b=$NPM_T 'BEGIN{printf "%+.2f", a-b}')s)"
check "consona overhead under 2s" awk -v a="$CON_T" -v b="$NPM_T" 'BEGIN{exit !(a-b<2)}'

if [ -n "${E2E_OUT:-}" ]; then mkdir -p "$E2E_OUT"; cp "$TMP"/*.out "$LOG" "$FLOG" "$E2E_OUT/" 2>/dev/null; cp "$D2LOG" "$E2E_OUT/daemon2.log" 2>/dev/null; cp "$TMP/fresh-home/.cache/consona/daemon.log" "$E2E_OUT/daemon-fresh.log" 2>/dev/null; fi
if [ "$FAILS" -eq 0 ]; then echo "E2E PASS"; else KEEP=1; echo "E2E FAIL ($FAILS); kept $TMP"; exit 1; fi
