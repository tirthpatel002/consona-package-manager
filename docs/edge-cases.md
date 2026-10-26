# Edge-case e2e suite (`e2e/edge.sh`)

`e2e/edge.sh` runs Consona end to end against 17 private fixture repos
(`consona-fx-*` on `$OWNER`, default `rakesh-jain-cb1998`). It needs
only a fixture-owner token, git, Go, node/npm, curl and python3. It installs
zero public packages.

```sh
# token for the fixture owner only, in this process env (never printed)
export CONSONA_TOKEN_RAKESH_JAIN_CB1998=...        # e.g. $(gh auth token -u rakesh-jain-cb1998)
EDGE_OUT=/tmp/edge-out bash e2e/edge.sh            # all lanes
bash e2e/edge.sh resolve repo                      # some lanes
```

Env: `OWNER` (fixture owner), `EDGE_PORT_BASE` (default 48760; ranges
overlapping 48730/48731 are refused), `EDGE_OUT` (copy of results/logs),
`KEEP=1` (keep the temp dir).

## Isolation and guards

* Binary built from this checkout into a temp dir; each lane has its own
  port, `HOME`, `CONSONA_HOME`, `CONSONA_CONFIG_DIR`, npmrc and npm cache;
  `consona setup --no-autostart` (no launchd/systemd unit); `setup --undo` at
  the end of every lane. On macOS nothing touches the Keychain (the token
  index is empty, the token comes from `CONSONA_TOKEN_<OWNER>`).
* The token must belong to the fixture owner (checked via `/user`, token sent
  to curl on stdin, never in argv). `GH_TOKEN`, `GITHUB_TOKEN`,
  `CONSONA_TOKEN` and every other `CONSONA_TOKEN_*` are unset.
* gh fallback guard: `GH_CONFIG_DIR` points at an empty dir and a tripwire
  `gh` shim is first on `PATH`; any call is logged and fails the run. Each
  lane checks the log store's `credential_source` is only
  `env CONSONA_TOKEN_<OWNER>`.
* Final token scan of all outputs: the token value and token-shaped strings
  (`gh[opsu]_…`, `github_pat_…`) must have 0 hits.

## Lanes (run in parallel)

| lane | port | cases |
|---|---|---|
| resolve | +0 | many-tags `^1 ^1.0.0 ~1.1.0 1.1.0 ^2 ^2.0.0 2.1.0-beta.1 * latest >=2.1.0-beta.0`, junk tags ignored, default saved range; no-v-tags; develop-only default branch → `0.0.0-<sha7>`; name mismatch |
| graph | +1 | deep private chain (chain-1 → chain-2 → chain-3, + lib-b); cycle a↔b terminates, installed once, daemon CPU idle afterwards; peer deps with / without lib-b / `--legacy-peer-deps` |
| pkg | +2 | bin via `.bin`, `npx`, `npx -y`; ESM import ok / require fails; monorepo root only; large files excluded (`files`); `.npmignore`; prepare+prepack outputs; build failure error quality and no partial install |
| repo | +3 | archived repo; renamed repo via old name (redirect) and new name; tag moved A→B: fresh resolve gets B, lockfile pinned to A fails with EINTEGRITY |
| lock | +4 | `consona install` of a mixed app; lockfile private entries → `127.0.0.1:<port>` only with sha512; warm `npm ci`; cold `npm ci` (empty consona + npm caches); deterministic rebuild; npm audit vs registry.npmjs.org |
| audit | +5 (+9 capture) | npm audit and a mixed bulk payload against a local capture upstream: no private name leaves the machine, public names still forwarded |

Every lane also checks `consona logs` (JSONL export and the human view) has a
`source.resolve` for each package it installed, and no gh-login credential.

Output: `results.tsv` (lane, case, PASS/FAIL/INFO, expected, actual), per-lane
npm output, `logs.jsonl`, `daemon.log`, `timings.txt`. Exit code 1 if any FAIL.

## Results

**With `fix/edge-bugs`** (2026-10-26, fix branch @ d4f8f86 + this branch,
VM linux/arm64 + macOS arm64): **131 PASS / 0 FAIL / 11 INFO** on both machines.
The 5 former FAILs below now pass, with stricter checks (see `e2e/edge.sh`).

**Before the fixes** (consona 0.10.0 @ 559b488): 119 PASS / 5 FAIL / 11 INFO,
identical on both machines. The FAILs were product findings, now fixed:

1. **cycle: daemon busy after install** — `warm.go` recursed with a fresh
   `seen` map per call, so a↔b warmed forever. Fixed: one visited set per
   warm walk plus a per-handler "warmed recently" TTL. Checks: daemon CPU
   < 2 CPU-s per 10 s window right after and 45 s after the install, and no
   new `source.resolve` events over 45 s.
2. **lockfile: no integrity for a package that needs a build** — the
   packument omitted `dist.integrity` for unbuilt versions. Fixed: the
   packument waits (bounded, `CONSONA_BUILD_WAIT`, default 90s) for the
   latest version's build. Checks: integrity on the first install, and
   unchanged after a 2nd install.
3. **build failure: script stderr not shown** — fixed: the error names the
   failing script and has the last 20 lines of (sanitized) build output.
   Checks: npm error says `the package's "prepare" script failed`, and both
   npm and the `consona install` summary carry the script's stderr.
4. **name mismatch is silent** (and repo renames) — fixed: a warning in the
   daemon log, the log store (`source.name_mismatch` / `source.redirect`) and
   the `consona install` summary. Checks: all three for name-mismatch and
   for the renamed repo (old name); `source.name_mismatch` for the new name.

Still INFO (by design): tag-moved shows npm's own EINTEGRITY (the daemon
warns with `source.tag_moved` only when it built that version from another
commit itself, which this suite does not do), and the other recorded
behaviours.

`creds.sh`: `E2E_NO_KEYCHAIN=1` skips scenario A and every Keychain call on
macOS (for Macs where no test item may be added to the login Keychain).
