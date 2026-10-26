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

## Known failures (2026-10-26, consona 0.10.0 @ 559b488, VM linux/arm64 + macOS arm64)

119 PASS / 5 FAIL / 11 INFO, identical on both machines. The FAILs are product
findings, not script errors:

1. **cycle: daemon busy after install** — `internal/adapter/npm/warm.go`:
   `warmRefs` → `Packages` → `warmDeps` → `warmRefs` recurses with a fresh
   `seen` map per call, so a↔b warms forever (≈1–2 cores, re-fetching from
   GitHub every memo TTL). Fix: share a visited set across one warm walk (or a
   per-handler "warmed recently" map keyed by package).
2. **lockfile: no integrity for a package that needs a build** —
   `registry.go` `Packument` omits `dist.integrity` for unbuilt versions; npm
   11 then writes no `integrity` for that entry (not even on a later install),
   so it is never verified on reinstall. Fix: when the packument is requested,
   wait (bounded) for the latest/prebuilt version's build and serve its hash,
   or serve only built versions' hashes once known and invalidate npm's cache.
3. **build failure: script stderr not shown** — `core` `BuildError` message
   is "npm install --include=dev failed; full output: <path>". Fix: include
   the last ~20 lines of the build output (prepare/prepack stderr) and name
   the failing script.
4. **name mismatch is silent** — `npmpack.Build` / `Packument` rewrite
   `name` to the requested package with no warning. Fix: log a warning event
   and print it in `consona install`/packument when the repo's manifest name
   differs.
