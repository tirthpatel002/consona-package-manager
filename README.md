# consona

**In two lines:** consona turns every package you own, wherever it lives, into a plain `npm install`: private GitHub repos, folders inside them (monorepos), folders on this machine, and packages on your other machines over SSH, all served as `@owner/name` from one local registry, with no publishing and no repo changes.
Run `consona setup` once: local copies win over remote ones so nothing is duplicated, and public npm keeps working exactly as before.

## Executive summary

- **Problem:** sharing private code as npm packages usually means running a private registry, publishing every change, or hand-writing git URLs and relative paths.
- **What consona does:** a background service on your machine answers npm first. `@owner/name` resolves straight from github.com/owner/name (versions from git tags, or `0.0.0-<commit>` without tags), with no changes to the repo. Registered local folders and packages on other devices over SSH work the same way, and local wins over GitHub, so nothing is duplicated.
- **What stays the same:** npm keeps all of its own features. Public packages, `npm login`, `npm publish` and `npm audit` go to your normal registry, and private names are never sent to it or published there.
- **Credentials:** it uses whatever GitHub access you already have (per-owner or named tokens, `GH_TOKEN`/`GITHUB_TOKEN`, or the `gh` login) and never requires one specific kind.
- **Trust and visibility:** builds run locally and are cached by commit, lockfiles get real integrity hashes, every command shows live verbose output (`--quiet` turns it off), and a structured log records everything without token values.
- **Platforms:** macOS and Linux tested; the same commands and lockfiles on every OS.
- **Examples:** step-by-step scenarios for every command are in [docs/examples.md](docs/examples.md).

## Install

You need Node.js/npm and one GitHub credential that can read your private
repos: a `gh auth login`, a token in an environment variable, or a named
token saved with `consona token add` (see [GitHub credentials](#github-credentials)).
The GitHub CLI is supported but not required.

```sh
curl -fsSL <release-url>/install.sh | sh -s -- --base-url <release-url>
```

This picks the right binary for your system (Linux or macOS, Intel or ARM),
checks it against the published SHA256 checksums and puts it in
`~/.local/bin` (`--system` installs to `/usr/local/bin` with sudo, `--dir`
anywhere else). On Windows, download `consona_<version>_windows_amd64.exe`
and put it on your PATH.

## Set up (once)

```sh
consona setup
```

This tells npm to ask consona first, and starts consona's small background
service now and at every login (it restarts itself if it ever crashes).
It finds the GitHub accounts and organisations you belong to and treats
`@<name>/...` packages from them as private. Add more with
`--scope @name`; use `--no-autostart` if you would rather start the service
yourself (`consona daemon start`).

## Use

Use npm as usual: `npm install`, `npm audit`, `npm publish`, `npm login`,
`npm whoami` ... For a private package, add it like any other:

```sh
npm install @your-github-name/your-repo
```

- Versions come from git tags (`v1.2.0` or `1.2.0`); a repo without tags is
  served from its default branch as `0.0.0-<commit>`.
- Packages that need building (TypeScript etc.) are built on your machine
  with their own `prepare`/`prepack` script, once per commit, then cached.
  npm's first request for such a package waits for the latest version's
  build (up to `CONSONA_BUILD_WAIT`, default 90s) so the lockfile gets its
  `integrity`. A failed build names the failing script and shows the last
  20 lines of its output (credentials removed); the full output is in the
  log file named in the error.
- `consona install` ends with a `consona: warning:` line when a repo was
  renamed or transferred (GitHub redirects the old name), when a
  package.json names a different package than the repo, or when a tag was
  moved after a lockfile recorded its old integrity.
- `consona install` is `npm install` that also prefetches private packages
  in parallel; it is a little faster but optional.

### New: lookalike guard, why/report, ci, dev, shared build cache, pnpm

Full examples, assumptions and sample output: [docs/new-features.md](docs/new-features.md).

- **Lookalike-name guard:** consona warns when a private name also exists on
  the public npm registry (a dependency-confusion risk). `consona check [dir]
  [--json]` audits a project; `CONSONA_STRICT=1` refuses to serve such names.
- **Provenance:** `consona why @scope/pkg [--json]` shows where an installed
  private package came from (source, repo, commit, tag, build time,
  integrity); `consona report [--json] [--out FILE]` does it for the whole
  lockfile. Both are read-only and offline.
- **CI:** `consona ci [npm ci args...]` runs `npm ci` with a temporary daemon,
  a temporary npmrc and full cleanup; it reads scopes and the port from
  `package-lock.json`. GitHub Actions: `uses: ./.github/actions/consona-ci`
  with a token in `env` (e.g. `CONSONA_TOKEN`).
- **Live local development:** `consona dev @scope/pkg` watches a registered
  local folder, rebuilds it and syncs it into the `node_modules` of projects
  that installed it (`--once`, `--project DIR`, `--list`).
- **Shared build cache:** packages that need a build are fetched already built
  from your SSH remotes when one has them (verified by sha512), else built
  locally. `consona cache list [--json]` and `consona cache prune
  [--older-than 7d] [--dry-run]` manage the local cache.
- **pnpm:** `consona install` / `consona audit` run pnpm when the project uses
  it (`packageManager` in package.json, or `pnpm-lock.yaml`); override with
  `--pm npm|pnpm` or `CONSONA_PM`.

## GitHub credentials

consona uses whatever credential is available and falls through to the
next one when a credential is rejected or cannot see a repo. For a package
from GitHub owner `<owner>` it tries, in order:

1. `CONSONA_TOKEN_<OWNER>` environment variable (owner uppercased, `-` becomes `_`,
   e.g. `CONSONA_TOKEN_ACME_CO` for `@acme-co`)
2. named tokens added with `--for @<owner>` (`consona token add`)
3. `CONSONA_TOKEN`
4. `GH_TOKEN`, then `GITHUB_TOKEN`
5. the default named token (`consona token add --default`)
6. every account gh is logged into for github.com (`gh auth token --user <login>`,
   honouring `GH_CONFIG_DIR`), see [Several gh accounts](#several-gh-accounts)

None of these is required; any one is enough. When a credential gets a 401,
a 404/403 or a rate limit, the next one is tried, and the one that worked
is used first for that owner from then on. If none works, the error (in
consona's output, npm's error and the daemon log) lists every source tried,
why it failed and how to fix it, for example:

```
consona: @acme/lib is private (scope @acme is routed to consona) but cannot read github.com/acme/lib with any available GitHub credential; tried:
  1. env CONSONA_TOKEN_ACME (ghp_…beef): 401 bad credentials: the token is invalid, revoked or expired (GitHub: Bad credentials).
     Fix: create a new token at https://github.com/settings/personal-access-tokens (fine-grained) or https://github.com/settings/tokens (classic), then update CONSONA_TOKEN_ACME in your environment
  2. named token "work" (--for @acme) (github_pat_…1a2b): 404: acme/lib does not exist or this token cannot see it.
     Fix: check the name; then at https://github.com/settings/personal-access-tokens edit the token: Resource owner must be @acme, Repository access must include acme/lib (or All repositories), Permissions > Contents: Read-only. For an organization, an owner may also need to approve fine-grained tokens (org Settings > Personal access tokens) and the token may need SSO authorization
  Not available:
    - gh login: gh is not installed (optional)
```

<a id="several-gh-accounts"></a>
**Several gh accounts.** When `gh auth status` shows more than one github.com
account, consona reads them all (from gh's `hosts.yml` or `gh auth status`,
with each token from `gh auth token --hostname github.com --user <login>`),
caches them in memory for a minute (and re-reads them after a 401), and
picks the account before the request:

- the account whose login equals the package owner (case-insensitive) first,
  so `@tirthpatel002/gradient` uses the tirthpatel002 account even when
  another account is active;
- for an organisation or another owner, the account remembered for that
  owner; the first time, consona probes `GET /repos/<owner>/<repo>` once with
  each account (active first) and remembers the first that can see it in
  `gh-accounts.json` in consona's config dir (owner -> login, no tokens);
  if it later fails, the others are tried and the mapping follows the one
  that works;
- then the active account, then the rest.

consona never runs `gh auth switch` and never writes gh's config: the active
account stays what you chose. Explicit credentials (1-5 above) still come
first. Verbose output, the daemon log and `consona logs --event credential`
say which account was used and why:

```
consona: @tirthpatel002/gradient via gh account tirthpatel002 (owner match)
consona: @acme-co/ui via gh account your-other-account (probed: first gh account that can see acme-co/ui; remembered for @acme-co)
```

If no account can read a repo, the error lists every gh account tried
(`gh account <login> (<why>) (gho_…abcd): 404 ...`). Needs gh 2.40 or newer
(multi-account `--user`); with an older gh only the active gh login is used,
as before.

Specific causes are recognised: bad or revoked tokens (401), expired
tokens (with the date), private repos the token cannot see (404: repo not
selected, missing Contents: Read, fine-grained tokens not approved by the
organization), SAML SSO (403 with the authorization link), rate limits
(with the reset time) and classic tokens without the `repo` scope.

### Named tokens

```sh
consona token add work --for @acme --description "Acme, read-only"   # hidden prompt
printf '%s' "$TOKEN" | consona token add ci --default                # or piped on stdin
consona token list
consona token test [name]      # without a name: every available credential
consona token remove work
```

The token is read from a hidden prompt or stdin, never from a command-line
argument (`--token=...` is refused, since arguments end up in shell history
and the process list). Names are unique; `--replace` overwrites one.

`token add` and `token test` show what GitHub reports: the account (login,
name, id), the expiry (`github-authentication-token-expiration`), the type
from the prefix (`ghp_` classic, `github_pat_` fine-grained, `gho_` OAuth,
`ghu_`/`ghs_` GitHub App), a classic token's scopes, the remaining rate
limit, then PASS/FAIL capability checks with a fix for each: list
accessible repos, and for every `--for` owner: see its repos, read
metadata, read contents, list tags. GitHub's API does not expose a
fine-grained token's name, description or permission list, so consona
checks what the token can actually do instead.

```
$ consona token add rakesh --for @rakesh-jain-cb1998
GitHub token for "rakesh" (input hidden):
consona: added token "rakesh" (gho_…Ab12) to the OS secure store; index: ~/.config/consona/tokens.json
  type:        OAuth app token (gho_, e.g. from `gh auth login`)
  account:     rakesh-jain-cb1998, id 347834475
  expires:     never (no expiry reported by GitHub)
  scopes:      gist, read:org, repo, workflow
  rate limit:  4987/5000 remaining (resets 09:32 UTC)
  for:         @rakesh-jain-cb1998
  PASS  list accessible repos (GET /user/repos): 22 repos visible
  PASS  @rakesh-jain-cb1998: repos visible: 22 repos (e.g. rakesh-jain-cb1998/consona)
  PASS  @rakesh-jain-cb1998: metadata (rakesh-jain-cb1998/consona): 200 OK
  PASS  @rakesh-jain-cb1998: contents read (rakesh-jain-cb1998/consona): 200 OK
  PASS  @rakesh-jain-cb1998: tags (rakesh-jain-cb1998/consona): 200 OK
```

Where things are kept:

- **Token values** only in the OS secure store, service `dev.consona.tokens`:
  macOS Keychain, Windows Credential Manager, Linux Secret Service
  (GNOME Keyring, KWallet, KeePassXC). The daemon reads them when a request
  needs them and keeps them in memory only (re-read at most once a minute,
  so `token add/remove` take effect without a restart).
  On macOS consona uses the login keychain only when HOME is your real home;
  with any other HOME it fails with a clear error instead of making macOS
  show a "Keychain Not Found" dialog. An explicit `GH_CONFIG_DIR` without a
  github.com login in its `hosts.yml` means "no gh login" (newer gh on macOS
  would otherwise answer from the keychain's default login).
- **The index** (names, owners, description, login, type, expiry, dates;
  never values) in `tokens.json` in consona's config dir, written
  atomically, file `0600`, dir `0700`: `~/.config/consona` (Linux),
  `~/Library/Application Support/consona` (macOS), `%AppData%\consona`
  (Windows), or `$CONSONA_CONFIG_DIR`.
- Without a secure store (e.g. a headless Linux server without a Secret
  Service), `token add` fails and says so; consona never falls back to a
  plaintext file. Use `CONSONA_TOKEN` / `CONSONA_TOKEN_<OWNER>` /
  `GH_TOKEN` or `gh auth login` there instead.

The auto-started service (systemd/launchd/logon task) does not see
environment variables from your shell: it uses named tokens and the gh
login (`GH_CONFIG_DIR` is passed to it if you set it when running setup).
For env-var-only setups, use `consona setup --no-autostart`, where the
daemon is started from your shell by `consona install` / `consona daemon start`.

## Logs

consona records what it does (resolves, fetches, builds, where each
private package came from (local folder, ssh remote or GitHub), register /
unregister, remote add / remove / test and ssh probe failures, credential
choices and failures, audit filtering, daemon start/stop) in a structured
log store, separate from the token index, so you can answer "why did this
install fail?" after the fact:

```sh
consona logs --errors --since 2h
consona logs --package @acme/lib --limit 20
consona logs --event credential --json      # credential.resolve / credential.fail
consona logs --event source                 # source.resolve (local|ssh|github) / source.fail
                                            # source.redirect / source.name_mismatch / source.tag_moved (warn)
consona logs --event ssh.probe --errors     # remotes that could not be reached
consona logs export --format jsonl -o consona-logs.jsonl
consona logs info                           # backend, path, counts, retention
consona logs prune                          # apply retention now
consona logs clear                          # asks first; --yes to skip
```

Filters: `--package`, `--owner`, `--event` (name or prefix: `build`,
`install`, `credential`, ...), `--level` (minimum), `--since`/`--until`
(`2h`, `7d`, `2026-10-26`, RFC 3339), `--errors`, `--limit`, `--json`.

- Storage: SQLite (pure Go, no cgo) at `<config dir>/logs/events.db` in
  WAL mode, so the daemon and CLI can write at the same time. If SQLite
  cannot be opened, created or migrated (locked, corrupt, or a binary built
  with `-tags nosqlite`), consona falls back to rotated JSON-lines files in
  `<config dir>/logs/jsonl/` (5 MB per file, file-locked) and logs why
  once. Force one with `CONSONA_LOG_BACKEND=sqlite|jsonl`.
- Each entry has: id, ts (UTC, RFC 3339 with nanoseconds), level, event,
  ecosystem, package, owner, repo, version, commit_sha, credential_source
  (the source's name/kind, never its value), result, duration_ms,
  http_status, error_code, message and extra fields as JSON. The schema is
  versioned and migrated on open.
- Every field passes through the redactor before it is written: token
  values never reach either backend (unit-tested for both).
- Retention: 30 days and about 50 MB by default
  (`CONSONA_LOG_MAX_AGE=14d`, `CONSONA_LOG_MAX_SIZE_MB=20`), applied when
  the daemon starts and every 6 hours, or with `consona logs prune`.
- The human-readable `daemon.log` stays as before (one line per request)
  and also gets a one-line version of each notable event, so both agree.
- Cost: writes are batched in the background (about 5-8 µs per event on
  the request path, flushed every 100 ms), so install time does not change
  measurably (e2e: warm install overhead vs plain npm +0.10 s, as before).
  Reads: `consona logs --package X` / `--errors` on 20,000 entries takes
  about 3 ms with SQLite and about 20 ms with the JSON-lines fallback
  (`go test -bench . ./internal/logstore`).
- Directory `0700`, files `0600`. `CONSONA_LOG_DIR` moves it.

## Config directory

Everything consona knows about *you* lives in one directory, `~/.config/consona`
(Linux, or `$XDG_CONFIG_HOME/consona`), `~/Library/Application Support/consona`
(macOS), `%AppData%\consona` (Windows), or `$CONSONA_CONFIG_DIR`:

| file | what | written by |
|---|---|---|
| `identity.json` | username + this device's id | `consona init` |
| `local-index.json` | registered folders (name -> path) | `register` / `unregister` |
| `remotes.json` | explicit ssh remotes | `remote add` / `remove` |
| `folder-map.json` | package name -> folder inside a GitHub repo | `map add` / `remove` / `sync` |
| `tokens.json` | named token index (never values; those are in the OS secure store) | `token add` / `remove` |
| `logs/events.db`, `logs/jsonl/` | event log (`CONSONA_LOG_DIR` overrides) | daemon and CLI |

The directory is `0700`, every file `0600`, and every write is atomic (a
synced temp file renamed over the old one), so a crash never leaves a
half-written index. Caches, builds, `daemon.log`, `remotes-state.json` and
ssh control sockets are disposable and stay in `CONSONA_HOME`.
`consona whoami`, `consona logs info` and `consona token list` print the
paths they use in the same `key: value` style.

## Local folders and other devices (SSH)

```sh
consona init --username tirth       # once per device: a free local name, scope @tirth
consona register ~/code/my-lib      # serve the folder live as @tirth/my-lib
consona remote add devbox devbox    # packages registered on a machine you can ssh into
npm install @tirth/my-lib           # this device's folders > ssh remotes > GitHub
```

Every install says where each package came from (`<- local ...`,
`<- ssh devbox ...`, `<- github ...`; mapped folders show the repo and
folder). Folders are versioned by content
hash (`1.0.0-local.3.h2cab0880`) and rebuilt only when they change. Hosts
in `~/.ssh/config` are found automatically. Details, versioning, caching,
timings and limits: [docs/local-and-ssh.md](docs/local-and-ssh.md).

## Folders inside GitHub repos (monorepos)

A repo can serve many packages: map a name to one folder of it, and
`npm install` fetches just that folder, with the repo's version tags as
the package's versions.

```sh
consona map add @acme/widgets-core acme/widgets packages/core
npm install @acme/widgets-core        # npm and pnpm, like any consona package
```

Mapped packages depend on each other by their normal names: when core
and ui are both mapped, `"@acme/widgets-ui": "^1.0.0"` in
`packages/core/package.json` resolves like any other private dependency
(`^1.0.0` needs a `v1.x` tag on the repo; see "Failures to expect"
below). Mapped scopes are routed to consona by `consona setup` like your
other scopes.

A repo that keeps a `consona.packages.json` at its root maps everything
it declares in one step:

```json
{"packages":[{"name":"@acme/widgets-core","path":"packages/core"},
             {"name":"@acme/widgets-ui","path":"packages/ui"}]}
```

```sh
consona map sync acme/widgets   # add, update and unmap to match the repo
consona map list                # what this device maps, and how (add | sync)
consona map remove @acme/widgets-core
```

Worked examples with sample output:
[docs/examples.md](docs/examples.md#210-serve-a-folder-of-a-repo-as-a-package-consona-map-add).

**By design**

- Versions are the repo's version tags, shared by every folder in it;
  per-folder versions are not supported. An untagged repo serves its
  default-branch head as `0.0.0-<commit>` (a prerelease). Tag the repo
  to give its folders real semver.
- The served name and version come from the map and the repo's tags, not
  from the folder's `package.json` (the same rule as a repo of its own).
  If `package.json` names something else, consona warns and code
  importing that other name will not find it.
- The folder is extracted alone and builds exactly as if it were a repo
  of its own: its `package.json` must sit at the folder root, and files
  outside the folder (root configs, a shared tsconfig, `file:` links out
  of the folder) are not present at install or build time.
- Only the mapped folder is written to disk, but the whole commit
  tarball is downloaded: the map chooses what is served, it is not
  access control. A credential that can read the repo can read every
  folder in it.
- A mapped name wins over ssh remotes and GitHub repos of the same
  name; a registered local folder still wins over the map. A mapping
  that fails (unreadable repo, folder missing at that commit) fails the
  install rather than falling through to a same-named repo.
- The build and manifest caches key on the mapped repo and folder as
  well as name, commit and version, so remapping a name rebuilds instead
  of serving the previous folder's bytes.
- Each package resolves and fetches at its own resolved commit; one
  install is not an atomic snapshot across packages.

**Failures to expect**

- On an untagged repo a sibling range like `^1.0.0` matches nothing (the
  served version is the `0.0.0-<commit>` prerelease) and npm fails with
  `ETARGET`. Use a range that admits it (for example `>=0.0.0-0`) or tag
  the repo `v1.x`.
- If the repo head moves between two packages' resolutions they can pin
  different commits. Tag the repo and depend on the tagged versions for
  a reproducible sibling set.
- The lockfile pins exact versions with their integrity: a moved tag or
  a remapped folder surfaces as a checksum mismatch (consona also warns
  when a tag points at another commit), never as silently different
  bytes. It detects the change rather than re-fetching the old bytes.
- A mapped folder without a `package.json` at its root fails the
  install, naming the repo, folder and commit.

**Not verified yet**

The mapped install, sibling dependencies and the remap case are proven
locally on one machine and one account with npm 10.9 and pnpm 12.15.
Real GitHub Actions CI and a second contributor's machine are untested;
treat those setups as unverified until they are run.

## Undo

```sh
consona setup --undo     # or: consona uninstall
```

npm goes back to exactly what it used before, the background service is
removed, and a login you made while consona was set up is kept.

## How it works

`consona setup` sets npm's registry to the local service
(`http://127.0.0.1:48731/npm/`) and remembers your previous registry
(normally registry.npmjs.org). For each request from npm:

- **Your private packages** are answered locally from GitHub with your
  GitHub credentials (see above). Their names are never sent to the public registry: not in
  installs, not in `npm audit` (they are removed from the audit request),
  and publishing them is refused (you release by pushing a git tag).
- **Public packages** are sent on to your previous registry, mostly as a
  plain redirect, so npm downloads them directly and lockfiles keep the
  usual `registry.npmjs.org` addresses.
- **Anything that needs your login** (publish, `npm whoami`, private npm
  packages, a company mirror) is passed on with your existing login for
  your previous registry, found the way npm finds it: `npm_config_*`
  environment variables, the project `.npmrc`, your `~/.npmrc` and npm's
  global npmrc, including `${NPM_TOKEN}`-style references. On Linux and
  macOS these come from the npm process that made the request (its
  environment and its project directory, found from the connection; only
  your own processes are read, and only for requests that need a login);
  if that process cannot be read, and on Windows, consona uses your
  `~/.npmrc`, the global npmrc and its own environment, and notes why in
  `consona logs --event peer.lookup`. consona never copies or logs your
  token; your npmrc only gets a placeholder value. Two-factor one-time
  passwords (`--otp` or the prompt) pass straight through. Only your own
  programs get your login: requests from other users on the same machine
  never do (Linux, macOS).
- **If the service is not running**, npm stops within a few seconds with
  `ECONNREFUSED 127.0.0.1:48731`; it never falls back to sending private
  names to the public registry. `consona daemon start` starts it.

Your GitHub tokens stay where they are (gh, your environment, the OS
secure store); consona only holds them in memory.

### Limits

- Auto-start is tested on Linux (systemd) and on macOS (Apple Silicon):
  the LaunchAgent works for setup, restart after `kill -9`, daemon
  stop/start, `setup --undo`, and reading tokens from the login Keychain.
  The Windows logon task is generated and unit-tested but not yet run on a
  Windows machine.
- On macOS and Windows the login lookup uses the service's own view of npm
  config (environment it was started with, `~/.npmrc`, global npmrc); a
  login that only exists in a project `.npmrc` or in your shell's
  environment is not seen there.
- The auto-started service does not see credential environment variables
  from your shell (`CONSONA_TOKEN*`, `GH_TOKEN`); it uses named tokens and
  the gh login. Use `--no-autostart` for env-var-only setups.
- Linux without a Secret Service (headless servers, minimal containers)
  cannot store named tokens; use the environment variables or gh there.

## Layout (extensible by adding packages, not editing core)

| package | role |
|---|---|
| `internal/core` | interfaces `Source`, `Builder`, `Adapter`; adapter registry; version rules; `Engine` (resolve + build + commit-keyed cache) |
| `internal/source/github` | `Source`: GitHub API with credential fallthrough, error classification, token inspection |
| `internal/creds` | credential resolution order, named-token index, OS secure store (go-keyring) |
| `internal/logstore` | structured event log: SQLite with JSON-lines fallback, retention, redaction |
| `internal/redact` | token redaction for every log and error |
| `internal/identity` | `consona init`: username + device id |
| `internal/localindex` | registered folders of this device |
| `internal/source/local` | `Source`: registered folders, content-hash versions |
| `internal/packmap` | the folder map: name -> GitHub repo folder (`folder-map.json`) |
| `internal/source/ghfolder` | `Source`: packages that are folders inside GitHub repos |
| `internal/source/sshremote` | `Source`: other devices over ssh (`remote-serve` protocol, ssh-config discovery, list cache) |
| `internal/source/chain` | resolution order local > ssh > GitHub, resolution events |
| `internal/localcli` | CLI for init/register/remote/map/which |
| `internal/builder/npmpack` | `Builder`: npm tarball via `npm pack` |
| `internal/adapter/npm` | `Adapter`: npmrc setup, npm registry protocol, login lookup, `npm` wrapper |
| `internal/daemon` | loopback server mounting each adapter at `/<name>/`, auto-start |
| `cmd/consona` | CLI |

A new tool (pnpm, pip, cargo, ...) is a new `Builder` + `Adapter` package
that calls `core.Register` and is imported in `cmd/consona`. A new code
location (local folder, SSH) is a new `Source`.

## Development

```sh
gofmt -l . && go vet ./... && go test -race ./...
e2e/fixtures.sh   # once: creates the private fixture repos
E2E_GH_CONFIG_DIR=~/.config/gh-fixture e2e/run.sh
                  # ~3 min end-to-end (Linux or macOS): real GitHub + npm, a
                  # token-requiring fake registry, a fresh HOME, release build
                  # + install.sh. Ports 48741-48749; the test unit gets only
                  # the gh config dir path, never a token.
CONSONA_TOKEN_<OWNER>=... E2E_NAMED_TOKEN=1 e2e/run.sh
                  # same, with gh switched off: the fixture token is stored as a
                  # named token in a test secure-store service
                  # (CONSONA_KEYRING_SERVICE, default a unique dev.consona.tokens.e2e-<pid>-<rand>)
                  # and removed afterwards; for a Mac with several gh accounts.
E2E_GH_CONFIG_DIR=~/.config/gh-fixture e2e/creds.sh
                  # credentials: no gh + named token in a throwaway Secret
                  # Service, CONSONA_TOKEN only, gh only, mixed fallthrough,
                  # all-rejected error, no secure store
scripts/release.sh 1.0.0   # dist/: binaries for linux/darwin amd64+arm64,
                           # windows amd64, SHA256SUMS, install.sh
```

Env:

- `CONSONA_PORT`: daemon port (default 48731).
- `CONSONA_HOME`: cache, build cache, `daemon.log`, remote state. Default is
  the OS cache dir (`os.UserCacheDir`) plus `consona`: `~/.cache/consona` on
  Linux (`$XDG_CACHE_HOME/consona` if set), `~/Library/Caches/consona` on
  macOS, `%LocalAppData%\consona` on Windows.
- `CONSONA_CONFIG_DIR`: identity, registered folders, remotes, token index, logs (see [Config directory](#config-directory)).
- `CONSONA_LOG_DIR`: log store directory (default `<config dir>/logs`).
- `CONSONA_KEYRING_SERVICE`: secure-store service name for named tokens (default `dev.consona.tokens`; tests use a throwaway one).
- `CONSONA_TOKEN`, `CONSONA_TOKEN_<OWNER>`, `GH_TOKEN`, `GITHUB_TOKEN`,
  `GH_CONFIG_DIR`: credentials (see [GitHub credentials](#github-credentials)).
- `CONSONA_LOG_BACKEND`, `CONSONA_LOG_MAX_AGE`, `CONSONA_LOG_MAX_SIZE_MB`: log store.
- `CONSONA_BUILD_WAIT`: how long npm's first request for a package with a
  build step waits for its latest version's build, so the packument carries
  `dist.integrity` (Go duration, default `90s`; `0` never waits).
- SSH options: see [docs/local-and-ssh.md](docs/local-and-ssh.md#environment).
