# consona

Use your private GitHub repos as npm packages, with no registry to run and
nothing to change in the repos. `npm install @your-github-name/your-repo`
just works for every repo your GitHub login can read. Everything else
(public packages, publishing, `npm login`) keeps working exactly as before.

## Install

You need Node.js/npm and the GitHub CLI (`gh`), logged in once with
`gh auth login`.

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
- `consona install` is `npm install` that also prefetches private packages
  in parallel; it is a little faster but optional.

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

- **Your private packages** are answered locally from GitHub through your
  `gh` login. Their names are never sent to the public registry: not in
  installs, not in `npm audit` (they are removed from the audit request),
  and publishing them is refused (you release by pushing a git tag).
- **Public packages** are sent on to your previous registry, mostly as a
  plain redirect, so npm downloads them directly and lockfiles keep the
  usual `registry.npmjs.org` addresses.
- **Anything that needs your login** (publish, `npm whoami`, private npm
  packages, a company mirror) is passed on with your existing login for
  your previous registry, found the way npm finds it: `npm_config_*`
  environment variables, the project `.npmrc`, your `~/.npmrc` and npm's
  global npmrc, including `${NPM_TOKEN}`-style references. consona never
  copies or logs your token; your npmrc only gets a placeholder value.
  Two-factor one-time passwords (`--otp` or the prompt) pass straight
  through. Only your own programs get your login: requests from other
  users on the same machine never do (Linux).
- **If the service is not running**, npm stops within a few seconds with
  `ECONNREFUSED 127.0.0.1:48731`; it never falls back to sending private
  names to the public registry. `consona daemon start` starts it.

Your GitHub token stays in `gh`; consona only holds it in memory.

### Limits

- Auto-start is tested on Linux (systemd). The macOS LaunchAgent and the
  Windows logon task are generated and unit-tested but not yet run on a Mac
  or Windows machine.
- On macOS and Windows the login lookup uses the service's own view of npm
  config (environment it was started with, `~/.npmrc`, global npmrc); a
  login that only exists in a project `.npmrc` or in your shell's
  environment is not seen there.
- The auto-started service needs `gh auth login` (a `GH_TOKEN` in your
  shell is not visible to it).

## Layout (extensible by adding packages, not editing core)

| package | role |
|---|---|
| `internal/core` | interfaces `Source`, `Builder`, `Adapter`; adapter registry; version rules; `Engine` (resolve + build + commit-keyed cache) |
| `internal/source/github` | `Source`: GitHub API via the gh login |
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
e2e/run.sh        # ~3 min end-to-end: real GitHub + npm, a token-requiring
                  # fake registry, a fresh HOME, release build + install.sh
scripts/release.sh 1.0.0   # dist/: binaries for linux/darwin amd64+arm64,
                           # windows amd64, SHA256SUMS, install.sh
```

Env: `CONSONA_PORT` (default 48731), `CONSONA_HOME` (default `~/.cache/consona`).
