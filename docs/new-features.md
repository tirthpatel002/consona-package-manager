# New features

Detailed examples for the lookalike-name guard, `consona why`/`report`, `consona ci`, `consona dev`, the shared build cache over SSH, and pnpm support. Each section says when to use it, what it assumes, the commands, what each part means (names like `@myorg`, `@acme/ui`, `laptop` or `7d` are your own choices), and what you'll see. Sample output is abridged; hashes, paths and timings will differ.

## Contents

- [Lookalike-name guard](#lookalike-guard)
- [consona why / report](#why)
- [consona ci](#ci)
- [consona dev](#dev)
- [Shared build cache](#build-cache)
- [pnpm support](#pnpm)
- [Several gh accounts](#gh-accounts)

<a id="lookalike-guard"></a>

## Lookalike-Name (Dependency Confusion) Guard

**When to use:**
Use this to protect against dependency confusion attacks where an attacker publishes a public package under the same name as one of your private routed packages. Run `consona check` in CI or locally to audit project dependencies before installing, or rely on the daemon's automatic background guard to warn or block (`CONSONA_STRICT=1`) during `npm install` / `consona install`.

**Assumptions:**
- Private packages belong to routed scopes configured in your user `.npmrc` by `consona setup` (or registered local/SSH remotes).
- The public registry checked is `https://registry.npmjs.org` by default, or overridden by `CONSONA_PUBLIC_REGISTRY`.
- Lookalike status answers are cached in `$CONSONA_HOME/lookalike.json` for 24 hours.
- Offline networks, timeouts, or public registry errors fail open so installations are not blocked when disconnected.

**Commands:**

```sh
# Check current directory dependencies for public lookalikes
consona check

# Check a specific project directory
consona check ./packages/web-app

# Output machine-readable JSON for CI pipelines
consona check --json

# Run quiet (suppress stderr progress, stdout results printed)
consona check --quiet

# Run npm install with strict lookalike enforcement (fails if lookalike exists)
CONSONA_STRICT=1 npm install
CONSONA_STRICT=1 consona install
```

**What each part means:**
- `check`: The sub-command that audits your project's manifests (`package.json` and `package-lock.json`).
- `[dir]`: Optional path to the project directory containing `package.json` (defaults to `.`). User-chosen path.
- `--json`: Optional flag to output structured JSON summary and per-package status on stdout.
- `--quiet`: Standard consona flag suppressing stderr progress lines (`say(...)`).
- `CONSONA_STRICT=1`: Environment variable (read from daemon or calling npm process environment) instructing the daemon to return HTTP 403 Forbidden for any private package that also exists on the public npm registry.

**What you'll see:**

When running `consona check` on a project with private packages:
```
consona: checking 2 private packages against registry.npmjs.org...
consona: checking @myorg/auth...
consona: checking @myorg/core...
@myorg/auth  LOOKALIKE  public registry has this name
@myorg/core  ok
consona: 1 lookalike(s) found across 2 private packages
```

When installing via `consona install` in warning mode:
```
consona: warning: @myorg/auth also exists on the public npm registry (registry.npmjs.org): a typo in your scope config or a missing scope route would install the public one; consona serves the private one
```

When installing with `CONSONA_STRICT=1`:
```
npm error code E403
npm error 403 Forbidden - GET http://127.0.0.1:48731/npm/@myorg/auth - consona: CONSONA_STRICT=1 is set and @myorg/auth also exists on the public npm registry (registry.npmjs.org) (potential dependency confusion); refusing to serve private package
```

<a id="why"></a>

## Package Provenance: consona why and consona report

**When to use:**
Use `consona why` to inspect where an installed package came from, how it was built, its exact git commit or content hash, tag, build timestamp, integrity hash, resolved URL, and any warnings recorded during its resolution and build.
Use `consona report` to generate a complete provenance audit report (as a Markdown table or JSON document) for all private packages in a project's `package-lock.json`.

**Assumptions:**
- You are in a project directory containing a `package-lock.json` file (or specify the directory with `--dir DIR`).
- Both `package-lock.json` v2/v3 and v1 formats are supported; `node_modules/<name>/package.json` is inspected if present.
- Both commands are strictly read-only and local: they do not contact the network, do not start the daemon, and do not create the log store if it is absent.
- If event logs or build cache metadata are missing for a package, "unknown" is reported for those fields rather than failing.

**Commands:**

Show provenance for a specific package:
```bash
consona why @acme/core
```

Show provenance with a specified project directory:
```bash
consona why @acme/core --dir /path/to/my-app
```

Output machine-readable JSON for a package:
```bash
consona why @acme/core --json
```

Generate a Markdown provenance report for all private packages in the project:
```bash
consona report
```

Save the provenance report to a file atomically:
```bash
consona report --out docs/provenance.md
```

Export private package provenance as a JSON array:
```bash
consona report --json --out provenance.json
```

**What each part means:**
- `<@scope/pkg>`: The full scoped or unscoped package name to inspect (e.g., `@myorg/auth` or `@local/tool`). (User-chosen package name).
- `--dir DIR`: The root path of the project containing `package-lock.json`. Defaults to `.` (the current working directory). (User-chosen path).
- `--json`: Format the output as JSON (a JSON object for `why`, a JSON array of objects for `report`).
- `--out FILE` (or `-o FILE`): File path where `consona report` will write its report atomically with permissions `0644`. If omitted or set to `-`, outputs to stdout. (User-chosen file path).
- `--quiet`: Suppress live progress lines on stderr (honours `CONSONA_QUIET=1`).

**What you'll see:**

Human-readable output (`consona why @acme/core`):
```text
consona: inspecting provenance for @acme/core in .
Package:    @acme/core
Version:    1.4.2
Source:     github
Repo:       acme/core
Commit:     7d8e9f0123456789abcdef0123456789abcdef01
Tag:        v1.4.2
Built:      2026-10-26T18:00:00Z
Integrity:  sha512-z4PhNX7vuL3xVChQ1m2AB9Yg5AULVxXcg/SpIdNs6c5H0NE8XYXysP+DGNKHfuwvY7kxvUdBeoGlODJ6+SfaPg==
Resolved:   http://127.0.0.1:48731/npm/@acme/core/-/@acme/core-1.4.2.tgz
Warnings:   none
```

Markdown table report (`consona report`):
```text
consona: generating provenance report for .
consona: found 2 private package(s)
# Package Provenance Report

| Package | Version | Source | Repo | Commit | Tag | Built | Integrity | Resolved | Warnings |
|---------|---------|--------|------|--------|-----|-------|-----------|----------|----------|
| @acme/core | 1.4.2 | github | acme/core | 7d8e9f0123456789abcdef0123456789abcdef01 | v1.4.2 | 2026-10-26T18:00:00Z | sha512-z4PhNX7vu... | http://127.0.0.1:48731/npm/@acme/core/-/@acme/core-1.4.2.tgz | none |
| @local/utils | 0.1.0-local.1.habcdef12 | local folder | /home/ubuntu/work/utils | abcdef1234567890 | unknown | 2026-10-26T17:30:00Z | sha512-m3Np8Q... | http://127.0.0.1:48731/npm/@local/utils/-/@local/utils-0.1.0-local.1.habcdef12.tgz | none |
```

<a id="ci"></a>

## Continuous Integration Runner (`consona ci` and GitHub Action)

**When to use:**
Use `consona ci` in CI/CD pipelines (such as GitHub Actions, GitLab CI, Buildkite, and Jenkins) to install private packages from GitHub repositories in an isolated, non-interactive environment. Unlike `consona setup`, which registers a persistent daemon and modifies `~/.npmrc`, `consona ci` runs an ephemeral daemon in-process, generates a temporary `.npmrc` isolated to the child process environment (`npm_config_userconfig`), executes `npm ci`, and guarantees complete cleanup on completion, error, or cancellation.

**Assumptions:**
- A `package-lock.json` or `npm-shrinkwrap.json` is present in the repository root or target directory (run `consona install` locally and commit the lockfile).
- GitHub repository read credentials are provided through the standard environment chain (`CONSONA_TOKEN_<OWNER>`, `CONSONA_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`, or `gh`). Credentials are never logged or stored.
- Node.js and npm are installed on the CI runner.

**Commands:**

```bash
# Standard CI installation: auto-discovers scopes and port from package-lock.json
consona ci

# Pass additional arguments directly to npm ci
consona ci --ignore-scripts --legacy-peer-deps

# Explicitly route scopes (in addition to lockfile-discovered scopes)
consona ci --scope @myorg --scope @partner

# Override the daemon port
consona ci --port 48825

# Quiet mode (suppresses progress output and verbose npm logs)
consona ci --quiet
```

**Using the GitHub Composite Action (`.github/actions/consona-ci`):**

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 20
      - name: Install dependencies with consona
        uses: ./.github/actions/consona-ci
        with:
          scopes: '@myorg, @partner'
          npm-args: '--ignore-scripts'
        env:
          CONSONA_TOKEN: ${{ secrets.CONSONA_TOKEN || secrets.GH_TOKEN }}
```

**What each part means:**
- `consona ci`: The command for automated CI runners.
- `--scope @owner`: Explicit scope routing flag (repeatable). User-chosen owner name (e.g. `@myorg`). Also configurable via the `CONSONA_SCOPES` environment variable (comma-separated).
- `--port N`: Binds the temporary daemon to a specific TCP port. If omitted, consona inspects `package-lock.json` for the port used by resolved URLs (defaulting to 48731) and falls back to an ephemeral port if busy.
- `--quiet`: Suppresses stderr progress output and runs npm at `--loglevel=notice` instead of `--verbose` (also honors `CONSONA_QUIET=1`).
- `[npm args...]`: Any additional arguments are forwarded directly to `npm ci` (e.g. `--ignore-scripts`, `--production`).
- Action inputs:
  - `version`: Release tag of consona to install (e.g. `v0.10.0` or `latest`, default: `latest`).
  - `binary-url`: Optional direct URL to download a custom consona binary.
  - `build-from-source`: Boolean (`true`/`false`, default: `false`). Builds consona from source using Go.
  - `npm-args`: Extra arguments passed to `npm ci`.
  - `scopes`: Additional private scopes to route to consona.
  - `working-directory`: Directory where `package-lock.json` is located.

**Lockfile Port Discovery & Registry Rewriting:**
`package-lock.json` records resolved tarball URLs pointing to `http://127.0.0.1:<port>/npm/...` from the developer's daemon. In CI:
1. consona inspects `package-lock.json` to discover private scopes and the daemon port used in resolved URLs.
2. Port selection: consona prefers the port recorded in the lockfile (default 48731) if it is free.
3. Rewriting on port clash: npm's default `replace-registry-host` config only rewrites `registry.npmjs.org`. If the lockfile port is busy, consona automatically selects a free ephemeral port and configures `replace-registry-host=always` in both the temporary `.npmrc` and `npm_config_replace_registry_host=always` in the child process environment. This instructs npm to rewrite resolved loopback URLs to the active daemon port seamlessly.

**What you'll see:**

```text
$ consona ci
consona: [daemon.start] INFO result=ok listening on 127.0.0.1:48731 (home /tmp/consona-ci-home-3177752070)
consona: daemon running on 127.0.0.1:48731 (temp home /tmp/consona-ci-home-3177752070)
consona: [ci.start] INFO result=ok starting ci run on port 48731 with scopes @myorg
consona: running: npm ci --verbose
...npm's verbose output, and one line per private package resolved and built...
consona: [audit.filter] INFO result=ok audit: forwarding 1 public package names, withheld 2 private
added 3 packages, and audited 4 packages in 3s
consona: [ci.finish] INFO result=ok ms=3135 npm ci exited with code 0
consona: npm ci exited with code 0 after 3.135s
consona: stopping daemon on 127.0.0.1:48731
consona: [daemon.stop] INFO result=ok stopped
```

When the lockfile's port is taken on the runner:

```text
consona: port 48731 from package-lock.json is busy; using free port 43865 with npm replace-registry-host
```

Without a lockfile:

```text
consona: consona ci needs a package-lock.json; run consona install locally and commit the lockfile
```

Nothing is left behind: the temporary daemon is stopped, the temp home and npmrc are removed, and `~/.npmrc` is never read for scopes or changed.

<a id="dev"></a>

## Live Local Development Sync (`consona dev`)

**When to use:**
Use `consona dev` when you are actively editing a local package registered on this device and want your changes reflected immediately in consuming projects without running `npm install` or `consona install` on every edit. It watches your source files, automatically runs lifecycle build scripts (`prepare` / `prepack`) when defined, and atomically synchronizes the resulting packed artifacts directly into consuming projects' `node_modules/@scope/pkg`.

**Assumptions:**
- The package folder has already been registered on this device via `consona register <folder>`.
- Consuming projects have already installed the package at least once through consona (the daemon records the project root in `dev-projects.json` when npm asks for the package, either its metadata or its tarball), or target project directories are explicitly specified via `--project <dir>`.
- Consuming projects contain a non-symlinked `node_modules/@scope/pkg` directory. If `node_modules/@scope/pkg` is a symlink, `consona dev` refuses to touch it to prevent accidental directory overwrites.

**Commands:**

```bash
# Watch a registered package and live sync into all tracked projects
consona dev @myorg/ui-kit

# Live sync into specific explicit project directories
consona dev @myorg/ui-kit --project /home/user/code/web-app --project /home/user/code/admin-portal

# Single build and sync pass (useful in CI or test scripts)
consona dev @myorg/ui-kit --once

# Adjust polling interval (default 500ms)
consona dev @myorg/ui-kit --interval 250ms

# List registered local packages and their tracked consuming projects
consona dev --list
```

**What each part means:**
- `@myorg/ui-kit`: The registered package name (user-chosen scoped name, matching a registered local folder in `consona registered`).
- `--project DIR`: Optional target project directory. Can be specified multiple times to target specific consuming applications instead of auto-discovering tracked projects.
- `--interval 500ms`: The polling interval for the change watcher. Defaults to `500ms`.
- `--once`: Runs an initial build and synchronization pass and exits immediately with code 0 (or 1 on failure), rather than entering a persistent watch loop.
- `--list`: Inspects all registered packages on this machine and displays every tracked consuming project recorded by the daemon, noting whether `node_modules/@scope/pkg` is currently present there.
- `--quiet`: Suppresses live stderr progress lines (or set `CONSONA_QUIET=1`).

**What you'll see:**

```text
$ consona dev @myorg/ui-kit
consona: dev: synced @myorg/ui-kit (18 files, 18 changed) -> /home/user/code/web-app/node_modules/@myorg/ui-kit in 185ms
consona: dev: watching /home/user/code/ui-kit (polling every 500ms) -> 1 project(s)
consona: dev: synced @myorg/ui-kit (18 files, 2 changed) -> /home/user/code/web-app/node_modules/@myorg/ui-kit in 42ms
^C
consona: dev: stopped watching @myorg/ui-kit
```

Listing tracked projects:

```text
$ consona dev --list
consona dev: registered local packages:
@myorg/ui-kit (/home/user/code/ui-kit):
  /home/user/code/web-app (node_modules present)
  /home/user/code/old-app (missing)
```

<a id="build-cache"></a>

## Shared Build Cache over SSH and Local Cache Management

**When to use:**
Use the shared build cache over SSH when you have multiple development machines (e.g., your laptop, workstations, devboxes, or CI runners configured in `~/.ssh/config` or via `consona remote add`) and want to avoid redundant local package builds. When a private package is built on one machine, other machines seamlessly fetch the compiled artifact over SSH rather than compiling from source, slashing install times from minutes to seconds.
Use `consona cache list` to inspect what build artifacts are stored in your local engine cache, their versions, commits, sizes, and build times.
Use `consona cache prune` to reclaim disk space by purging older cached builds (e.g., older than 30 days or a custom duration like 7d, 24h, or 30m).

**Assumptions:**
- Remote machines run consona (`consona remote-serve`) and are configured in consona either explicitly via `consona remote add <name> <target>` or automatically via concrete Host entries in `~/.ssh/config`.
- SSH access uses existing SSH keys or agents (consona runs ssh in `BatchMode=yes` with standard host key checking).
- When a package revision (commit-addressed for GitHub or content-hash addressed for local folders) needs building, consona queries configured remotes in Manager order with a short per-remote timeout (5s for initial reply, up to 2m for tarball transfer). Only packages that require a build step query remotes.
- **Integrity & Trust Model:** A remote is a machine the user has configured access to; nevertheless, consona verifies integrity end-to-end. Every fetched artifact is checked against the SHA-512 announced by the remote, the hash is recomputed locally from raw bytes, and the archive is inspected to ensure it is a valid gzip tarball containing `package/package.json` with a matching package name. Furthermore, npm's lockfile integrity verification also checks the downloaded tarball before installation.
- Fail-open resilience: if any remote is unreachable, offline, corrupted, or returns a hash mismatch, consona immediately logs the issue at debug level and falls back to building the package locally from source without failing the installation.

**Commands:**

List local build cache entries (human-readable table, newest first):
```bash
consona cache list
```

List local build cache entries as JSON (with total size and metadata):
```bash
consona cache list --json
```

Preview what would be removed by pruning entries older than 30 days (default) without deleting:
```bash
consona cache prune --dry-run
```

Prune cache entries older than 7 days:
```bash
consona cache prune --older-than 7d
```

Prune cache entries older than 24 hours (dry run):
```bash
consona cache prune --older-than 24h --dry-run
```

**What each part means:**
- `list` (or `ls`): Lists all cached package build artifacts located under `$CONSONA_HOME/cache` (newest first), detailing package name, version, commit SHA, file size, build timestamp, and local path, followed by a total size summary.
- `--json`: Formats output as structured JSON containing `entries` and `total_size`.
- `prune`: Deletes cached artifact directories whose built time/mtime is older than the specified retention window, cleaning up empty parent directories up to the cache root.
- `--older-than D`: Retention threshold duration. Accepts standard durations and days syntax (e.g., `30d`, `7d`, `24h`, `30m`). Defaults to `30d`.
- `--dry-run`: Performs a trial run without deleting any files from disk, reporting which entries would be removed and how much disk space would be freed.
- `--quiet`: Suppresses non-essential live progress lines on stderr (honours `CONSONA_QUIET=1`).
- User-chosen names:
  - `@acme/ui`, `@acme/core`: Example package names representing your private packages.
  - `laptop`, `vm`: User-chosen remote names configured via `consona remote add <name> <target>` or host aliases from `~/.ssh/config`.
  - `7d`, `24h`, `30m`: User-chosen duration thresholds for cache pruning.

**What you'll see:**

Live build cache hit during `consona install`:
```text
consona: build cache: got @acme/ui@1.2.0 (abc1234) from ssh remote laptop in 0.8s
```

Listing cache entries (`consona cache list`):
```text
PACKAGE    VERSION  COMMIT   SIZE      BUILT                PATH
@acme/ui   1.2.0    abc1234  42.5 KB   2026-10-26 18:30:12  /home/ubuntu/.cache/consona/cache/npm/acme/ui/abc123456789/1.2.0/acme-ui-1.2.0.tgz
@acme/core 2.0.0    9876543  118.2 KB  2026-10-26 17:15:00  /home/ubuntu/.cache/consona/cache/npm/acme/core/9876543210ab/2.0.0/acme-core-2.0.0.tgz

Total size: 160.7 KB (2 entries)
```

JSON listing (`consona cache list --json`):
```json
{
  "entries": [
    {
      "package": "@acme/ui",
      "version": "1.2.0",
      "commit": "abc123456789",
      "size": 43520,
      "built": "2026-10-26T18:30:12Z",
      "path": "/home/ubuntu/.cache/consona/cache/npm/acme/ui/abc123456789/1.2.0/acme-ui-1.2.0.tgz"
    }
  ],
  "total_size": 43520
}
```

Pruning cache (`consona cache prune --older-than 7d`):
```text
consona: removed @acme/old-lib@0.9.0 (def4567, 85.3 KB)
consona: cache prune: freed 85.3 KB (1 entry)
```

Pruning with `--dry-run`:
```text
consona: would remove @acme/old-lib@0.9.0 (def4567, 85.3 KB, built 2026-10-15 10:20:00)
consona: cache prune: would free 85.3 KB (1 entry) (dry run)
```

<a id="pnpm"></a>

## pnpm Package Manager Support

**When to use:**
Use consona with `pnpm` in your JavaScript and TypeScript projects. Consona automatically detects projects configured for `pnpm` (via `"packageManager": "pnpm@..."` in `package.json` or the presence of `pnpm-lock.yaml`) and runs `pnpm` with live output, daemon prefetching/warming, private package resolution from GitHub and local folders, lockfile integrity recording, and audit filtering. You can also explicitly choose or override the package manager using `--pm pnpm` / `--pm npm` or the `CONSONA_PM` environment variable.

**Assumptions:**
- `consona setup` has been run on the machine (writing registry, private scopes, and token placeholder lines to `~/.npmrc`, which pnpm reads natively).
- When pnpm is selected, `pnpm` is installed and accessible on PATH (e.g. via `corepack enable` or `npm i -g pnpm`). If pnpm is not on PATH, consona exits with a clear error: `"consona: pnpm not found; install it (corepack enable / npm i -g pnpm) or use --pm npm"`.
- The project's package manager is detected by checking:
  1. Explicit `--pm npm|pnpm` CLI flag (highest priority).
  2. `CONSONA_PM` environment variable (`CONSONA_PM=pnpm` or `CONSONA_PM=npm`).
  3. `package.json` `"packageManager"` field (e.g. `"pnpm@9.12.0"` -> pnpm, `"npm@..."` -> npm).
  4. Existence of `pnpm-lock.yaml` in the project root -> pnpm.
  5. Default fallback -> npm.
- During installation, private dependencies are prefetched and warmed in the background while pnpm resolves and downloads packages.
- Tarball integrity entries (`sha512-...`) are recorded into `pnpm-lock.yaml` matching what the consona daemon serves.
- Do not switch package managers over an existing `node_modules`; remove it first (npm over a pnpm `node_modules` can fail with a 404 for a public package).
- `consona audit` forwards public packages to the upstream registry while withholding private package names, returning clean advisory reports.

**Commands:**

Install packages in a pnpm project (auto-detected via `package.json` or `pnpm-lock.yaml`):
```bash
consona install
```

Install packages explicitly selecting pnpm:
```bash
consona install --pm pnpm
```

Install a private dependency with pnpm:
```bash
consona install --pm pnpm @myorg/private-pkg
```

Audit packages in a pnpm project with private names withheld:
```bash
consona audit
```

Audit packages with an explicit package manager override:
```bash
consona audit --pm pnpm
```

Run quiet install (suppressing progress lines and running pnpm without verbose flags):
```bash
consona install --quiet
```

Select pnpm via environment variable:
```bash
CONSONA_PM=pnpm consona install
```

**What each part means:**
- `consona install`: Runs the package manager's install command (`npm install` or `pnpm install`) after verifying setup and ensuring the daemon is running.
- `consona audit`: Runs security audit (`npm audit` or `pnpm audit`) against the daemon, which strips private packages from the audit payload before querying upstream advisories.
- `--pm npm|pnpm`: Consona flag that explicitly selects the package manager to use, overriding environment variables, `package.json`, and lockfiles. This flag is consumed by consona and not passed to npm/pnpm.
- `CONSONA_PM`: Environment variable (`CONSONA_PM=pnpm` or `CONSONA_PM=npm`) providing an explicit default package manager for all consona commands in the environment.
- `--quiet`: Suppresses consona progress output on stderr and executes the package manager with default quiet loglevel/reporter.
- `<@scope/pkg>`: User-chosen package specifier to install (e.g. `@myorg/auth@^1.0.0`).

**What you'll see:**

Installing with auto-detected pnpm:
```text
consona: npm is set up for consona (/home/you/.npmrc)
consona: using pnpm (pnpm-lock.yaml)
consona: running: pnpm install --reporter=append-only --loglevel=info (private scopes via http://127.0.0.1:48731/npm/, everything else via https://registry.npmjs.org)
...pnpm's progress lines, and consona's lines for each private package resolved and built...
Done in 5.7s using pnpm v10.x
consona: pnpm install exited with code 0 after 5.8s
```

The reason in brackets is how pnpm was chosen: `--pm flag`, `CONSONA_PM`, `package.json packageManager` or `pnpm-lock.yaml`.

Audit with pnpm (private names never leave the machine):
```text
consona: using pnpm (package.json packageManager)
consona: [audit.filter] INFO result=ok audit: forwarding 3 public package names, withheld 4 private
No known vulnerabilities found
consona: pnpm audit exited with code 0 after 1.2s
```

If pnpm is selected but missing from PATH:
```text
consona: pnpm not found; install it (corepack enable / npm i -g pnpm) or use --pm npm
```

<a id="gh-accounts"></a>

## Several gh Accounts

**When to use:** `gh` is logged into more than one github.com account (for example a work account active and your own account added with `gh auth login`), and a private package belongs to an account that is not the active one. Before, consona used only the active account and got a 404.

**Assumptions:**
- gh 2.40 or newer (`gh auth token --user`). With an older gh, consona uses the active gh login only, as before.
- Explicit credentials (`CONSONA_TOKEN_<OWNER>`, named tokens, `CONSONA_TOKEN`, `GH_TOKEN`/`GITHUB_TOKEN`, the default named token) still come first.
- consona only reads gh: it never runs `gh auth switch` or writes gh's config.

**Commands:**

```sh
gh auth status                     # lists every account; one is active
consona install                    # nothing to configure
consona logs --event credential    # which account each owner used, and why
cat ~/.config/consona/gh-accounts.json   # Linux path; the remembered org -> account mapping (no tokens)
```

**What each part means:**
- `owner match`: the gh account whose login equals the package owner (case-insensitive) is used first, with no probe and no 404 first.
- `probed ... remembered for @<owner>`: for an organisation (or any owner that is not a gh login) consona asks `GET /repos/<owner>/<repo>` once with each account, active first, and stores the first that can see it in `gh-accounts.json` in consona's config dir (`~/.config/consona` on Linux, `~/Library/Application Support/consona` on macOS, or `$CONSONA_CONFIG_DIR`). Later requests use it directly; if it stops working the other accounts are tried and the mapping is updated.
- Accounts are read once a minute at most (and again after a 401), in the daemon's memory only.

**What you'll see:**

```
consona: @tirthpatel002/gradient via gh account tirthpatel002 (owner match)
consona: @acme-co/ui via gh account work-account (probed: first gh account that can see acme-co/ui; remembered for @acme-co)
```

When no account can read the repo, the error lists each one:

```
  3. gh account pateltirth2 (active gh account) (gho_…vE2w): 404: acme-co/secret does not exist or this token cannot see it.
  4. gh account tirthpatel002 (other gh account) (gho_…k9Qd): 404: ...
```

