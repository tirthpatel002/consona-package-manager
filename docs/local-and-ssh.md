# Local folders and SSH remotes

Besides private GitHub repos, consona can serve npm packages from
**folders on this device** and from **folders registered on other devices
you can `ssh` into**. No registry, no publishing, no repo setup.

```sh
consona init --username tirth          # once per device: your scope @tirth + a device id
consona register ~/code/my-lib         # serve it as @tirth/my-lib (live)
consona registered                     # what this device serves
npm install @tirth/my-lib              # or: consona install @tirth/my-lib
consona which @tirth/my-lib            # where a name would come from, and how long it took
consona unregister @tirth/my-lib       # the folder itself is never touched
```

`consona install` prints where every consona-served package came from:

```
consona: source: @tirth/my-lib@1.0.0-local.1.h5d01f4f5 <- local /home/tirth/code/my-lib [device laptop (6bf1a000)]
consona: source: @tirth/ts-lib@0.1.0-local.1.h201c567f <- ssh devbox (from ssh config) [device devbox (248cea86)]
consona: source: @acme/util@0.0.0-1282889 <- github github.com/acme/util
```

## Identity

- `consona init` asks for (or takes `--username`) a free local name. It is
  not a GitHub or npm account; it is only the default scope `@name` for your
  registered folders. Changing it later needs `--force`.
- Each device gets a random device id plus its hostname/OS/arch. Packages
  always show which device served them.
- Stored in `$CONSONA_CONFIG_DIR` (default: `~/.config/consona` on Linux,
  `~/Library/Application Support/consona` on macOS, `%AppData%\consona` on
  Windows): `identity.json`, `local-index.json` (registered folders) and
  `remotes.json`, 0600.
- `consona setup` works without a GitHub login (devices that only use local
  folders and ssh remotes); it then routes `@username` and the scopes of
  your registered folders to consona.

## Registering folders

- The default name is `@<username>/<package.json name without scope>`. If
  the package.json says something else, the packed package.json is renamed
  (your folder is never written to: no node_modules, lockfile or build
  output is left behind).
- `--name @scope/pkg` picks the name. A name in a scope that is already
  private (your GitHub owners, your username) is fine and **shadows** the
  GitHub repo on this device. A name in any other scope (e.g. `@types/...`)
  would hide public packages, so it needs `--allow-scope`.
- No duplicates per device: one folder = one name and one name = one
  folder (symlinks resolved). `--replace` re-points or renames.
- Registering a folder routes its scope in your npmrc (only if `consona
  setup` was run).

## Versions and caching (live folders)

A registered folder is always served in its **current** state:

- consona hashes the folder (sha256 over relative paths, the executable bit
  and contents (file hashes are memoised by size/mtime); `node_modules`, `.git`, `.hg`, `.svn`, OS junk files and
  symlinks are skipped). The hash takes a few ms for a normal package.
- The version is `<package.json version>-local.<n>.h<hash8>`, e.g.
  `1.0.0-local.3.h2cab0880` (a prerelease base becomes
  `1.0.0-beta.1.local.3.h2cab0880`). `n` goes up by one each time the
  content (or the package.json version) changes, so the newest folder
  state is always the highest version and npm's semver logic works.
  The counter lives in `$CONSONA_HOME/local/<scope>/<name>.json`.
- The build (the package's own `prepare`/`prepack`, then `npm pack`) runs
  once per content hash and is cached like a GitHub commit. Unchanged
  folder = instant reinstall; changed folder = one rebuild.
- Lockfiles keep working: an older `-local.N` version still in a lockfile is
  served from the cache as long as it is there.
- `consona install` with no arguments notices when a locally registered
  dependency changed and runs `npm update <names>` for you, so editing the
  folder and running `consona install` picks up the change. Plain
  `npm install` keeps the lockfile version (npm semantics); use
  `npm update @tirth/my-lib` or `consona install @tirth/my-lib@latest`.

## SSH remotes

A remote is another device that has consona installed and folders
registered. Its packages install here over `ssh`; the build happens on the
remote (with the remote's own toolchain and npm config) and the tarball is
streamed back and verified (size, sha512, sha1).

```sh
consona remote add devbox tirth@10.0.0.5 [--port 22] [--identity ~/.ssh/id_ed25519] [--remote-cmd /opt/bin/consona]
consona remote add devbox devbox        # an alias from ~/.ssh/config
consona remote list                     # status, packages, last check
consona remote test [devbox]
consona remote remove devbox
```

- **Auto-detection:** every concrete `Host` alias in `~/.ssh/config` (and
  its `Include`s) is also tried, after explicit remotes. Wildcard hosts and
  git forges (github.com, gitlab.com, bitbucket.org, ...) and `User git`
  hosts are skipped. Turn
  it off with `CONSONA_SSH_AUTODETECT=0`. Duplicates (same name or target)
  are dropped.
- consona runs the system `ssh` in BatchMode (it never prompts: keys or an
  agent must already work for `ssh host true`) with a 3 s connect timeout.
  Host keys are checked exactly as your ssh config says; consona never
  changes `StrictHostKeyChecking` or `known_hosts`.
- On the remote it runs `consona remote-serve` (a tiny JSON-lines protocol,
  `consona-remote/1`). Non-interactive shells often lack `~/.local/bin` or
  nvm; consona adds the usual locations. If it still is not found you get
  a clear error; use `--remote-cmd` (or `CONSONA_REMOTE_CMD`).
- Remote packages are versioned exactly like local ones (the remote's
  `-local.N.hHASH` version), and cached here per remote + content hash.
- Remote package scopes are routed in your npmrc when `remote add`
  succeeds; for auto-detected hosts, add the scope once with
  `consona setup --scope @name` (or `remote add` the host explicitly).

## Resolution order

For a name in a private scope:

1. **This device's registered folders.** If the folder is broken (bad
   package.json, build error) you get that error; consona does not fall
   back to another source silently.
2. **SSH remotes** (explicit first, then auto-detected), using cached
   package lists. GitHub is asked in parallel so a slow remote never adds
   GitHub's latency on top.
3. **GitHub** (as before).
4. Not found: the error lists every place consona looked.

Names in scopes that are not private are never looked up locally or over
ssh; they go to your previous registry exactly as before.

## Performance

Measured with Linux on both ends (an ARM VM as the package host, an x86
machine over the internet as the ssh client), Node 24 / npm 11:

| case | time |
|---|---|
| local name lookup (`consona which`) | 0.04 s, no network |
| first install of a JS + TS folder (includes `tsc`) | 3.4 s |
| reinstall, folder unchanged (lockfile) | 0.4 s |
| folder edited, `consona install` (rehash + rebuild + `npm update`) | 1.0 s |
| install 2 packages from a remote over ssh (cold, remote builds TS) | 3.6 s |
| reinstall from lockfile, remote packages | 0.4 s |
| `remote test` round trip | 0.5 to 0.7 s |
| GitHub name, one **unreachable** ssh-config host | 3.1 s once, then 0.5 s |
| unknown name, 2 live + 1 down + 1 broken remote | 3.1 s first, 0.1 s after |

Why it stays fast:

- **Package lists are cached** per remote (fresh for 60 s, used for 30 min
  while refreshed in the background, persisted in
  `$CONSONA_HOME/remotes-state.json`). The daemon warms them at start.
- **A down host costs one connect timeout (3 s), then is skipped for 5
  min**; a host without consona is skipped for 30 min. Remotes are queried
  in parallel and GitHub in parallel with them.
- **SSH connection reuse** (OpenSSH ControlMaster, sockets in
  `$CONSONA_HOME/ssh`, kept 60 s) makes follow-up requests ~1 RTT.
  Disable with `CONSONA_SSH_CONTROL=0`.
- Only names in private scopes ever reach this code path.

## Limits

- **Windows:** OpenSSH for Windows has no ControlMaster, so every remote
  request opens a new ssh connection (slower, still correct). Not yet run on
  Windows/macOS hardware; Linux to Linux is tested end-to-end.
- **The remote needs consona** on its non-interactive PATH (or
  `--remote-cmd`). There is no agentless mode.
- The remote builds with its own npm config and toolchain; a package that
  needs private dependencies must be installable on the remote.
- Remote packages are not auto-updated by `consona install`; run
  `npm update <name>` (local folders are auto-updated).
- Only npm is wired up; the source layer is tool-agnostic.

## Integration hooks

- `chain.EventSink` (`Resolved(Resolution)`) receives one event per
  resolution (name, version, source kind, location, device, duration,
  error). It is the place to plug a log store; the daemon exposes the
  recent ones at `GET /-/consona/resolutions?since=<unix-ns>` (loopback
  only) for the CLI's "source:" lines.
- `core.Live` (sources whose answer changes without a commit: skip the
  resolve memo) and `core.Prebuilt` (sources that hand back a finished
  tarball, e.g. ssh) are optional interfaces on `core.Source`.

## Environment

| variable | meaning |
|---|---|
| `CONSONA_CONFIG_DIR` | identity + registered folders + remotes |
| `CONSONA_HOME` | caches, version counters, remote state, ssh sockets |
| `CONSONA_SSH_CONFIG` | ssh config file to use (default: ssh's own) |
| `CONSONA_SSH_AUTODETECT=0` | do not try `~/.ssh/config` hosts |
| `CONSONA_SSH_CONTROL=0` | no ControlMaster connection reuse |
| `CONSONA_REMOTE_CMD` | consona command on remotes (default: search PATH, `~/.local/bin`, nvm) |
