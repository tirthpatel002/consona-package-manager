# Consona examples

Every example below has the same shape: **When to use**, **Setup / assumptions**
(which machine, which folders, which account), the commands with comments,
**What each part means**, and **What you'll see / what happens**.

How to read the commands:

- **Consona keyword** = a word you type exactly as shown (`consona`, `setup`,
  `register`, `remote add`, `--scope`, ...).
- **Your choice** = a name you make up or a value from your own setup. The
  examples use neutral placeholders; replace them with your own:

  | placeholder | what it stands for |
  |---|---|
  | `your-github-name` | your GitHub user name (the owner part of `github.com/your-github-name/...`) |
  | `your-repo`, `your-lib` | a repository you own on GitHub |
  | `acme-co` | a GitHub organisation you belong to |
  | `alice` | your **consona username**, chosen with `consona init` (not a GitHub or npm account) |
  | `work-laptop` | a **remote name**: a short nickname you pick for another machine |
  | `alice@203.0.113.10` | an ssh login on another machine (`user@host`) |
  | `my-app`, `date-utils` | example project and package folders |

- Sample output is from a real run of consona 0.10.0 on Linux, shortened
  (npm's own `npm verbose` / `npm http` lines are left out). Paths, device
  ids, versions and timings will differ on your machine. The daemon's
  default address is `127.0.0.1:48731` (`CONSONA_PORT` changes it).

Contents:

1. [Getting started](#1-getting-started): set up once, check it works, undo
2. [Private GitHub packages](#2-private-github-packages): install, versions, releases, builds, private dependencies, several accounts, warnings, monorepo folders (`consona map`)
3. [Local packages on the same machine](#3-local-packages-on-the-same-machine): `init`, `register`, live folders, names, `which`
4. [Packages on other machines over SSH](#4-packages-on-other-machines-over-ssh): remotes, ssh config hosts, resolution order
5. [GitHub credentials](#5-github-credentials-in-practice): order, per-owner variables, named tokens, gh, CI, errors
6. [Everyday npm](#6-everyday-npm-with-consona): plain npm, `consona install`/`audit`, publish, login, daemon down
7. [Visibility and troubleshooting](#7-visibility-and-troubleshooting): verbose/quiet output, logs, daemon, version

## 1. Getting started

### 1.1 Set up consona once on a machine

**When to use:** the first time you use consona on a machine, after the
`consona` binary is on your PATH (see [Install](../README.md#install)).

**Setup / assumptions:** your own laptop; Node.js/npm installed; one
GitHub credential that can read your private repos (for example you ran
`gh auth login` as `your-github-name`, who is also a member of the
`acme-co` organisation).

```sh
consona setup                      # point npm at consona and start it now and at every login
```

**What each part means:**

- `consona` – the consona program (keyword).
- `setup` – keyword: the one-time setup command. It finds the GitHub
  accounts and organisations your credential belongs to, writes a small
  marked block into your user npmrc (`~/.npmrc`), installs auto-start
  (systemd user unit on Linux, LaunchAgent on macOS, logon task on
  Windows) and starts the daemon.
- Optional keywords: `--scope @name` (route one more owner, see
  [2.7](#27-several-github-accounts-or-organisations---scope)) and
  `--no-autostart` (do not install auto-start; you start the daemon
  yourself, see [5.5](#55-ci-and-env-var-only-machines)).

**What you'll see / what happens:**

```
consona: finding your GitHub owners (credential order: CONSONA_TOKEN_<OWNER>, named tokens, CONSONA_TOKEN, GH_TOKEN, GITHUB_TOKEN, gh login)
consona: GitHub owners: @your-github-name, @acme-co
consona: writing the npm config (registry http://127.0.0.1:48731/npm/, private scopes, retry timeouts, loglevel=verbose unless you set a loglevel)
consona: npmrc /home/alice/.npmrc:
consona:   + # >>> consona: npm asks the local consona daemon first (managed by `consona setup`, undo: `consona setup --undo`) >>>
consona:   + registry=http://127.0.0.1:48731/npm/
consona:   + //127.0.0.1:48731/npm/:_authToken=[redacted]
consona:   + fetch-retry-mintimeout=500
consona:   + fetch-retry-maxtimeout=2000
consona:   + @your-github-name:registry=http://127.0.0.1:48731/npm/
consona:   + @acme-co:registry=http://127.0.0.1:48731/npm/
consona:   + loglevel=verbose
consona:   + # <<< consona <<<
consona: installing auto-start (start at login, restart on crash)
...
consona: npm now asks http://127.0.0.1:48731/npm/ first (/home/alice/.npmrc): @your-github-name, @acme-co served by consona (this device's folders, ssh remotes, GitHub), everything else goes to your previous registry
```

The `_authToken` line in the npmrc is only a placeholder
(`consona-uses-your-registry-login`), never a real token.

### 1.2 Check that it works

**When to use:** right after setup, or whenever you are unsure whether
consona is running and which identity/credentials it sees.

**Setup / assumptions:** same laptop as 1.1.

```sh
consona daemon status              # is the background service up, and where is its log?
consona token test                 # which GitHub credentials work (no name = test every available one)
consona version                    # which consona build you have
```

**What each part means:**

- `daemon status` – keywords: checks the daemon and starts it if it is not
  running (same as `daemon start`).
- `token test` – keywords: checks credentials against GitHub; with a name
  (`consona token test work`) it tests only that named token.
- `version` – keyword: prints the version.

**What you'll see / what happens:**

```
$ consona daemon status
consona: daemon healthy on 127.0.0.1:48731
consona: daemon running on 127.0.0.1:48731 (log /home/alice/.cache/consona/daemon.log)
$ consona version
consona 0.10.0
```

### 1.3 Set up a machine that has no GitHub access

**When to use:** a machine that only uses its own folders or other
machines over ssh (sections 3 and 4), with no GitHub credential at all.

**Setup / assumptions:** any machine; no `gh` login and no token
variables; you already ran `consona init --username alice` (see
[3.1](#31-give-this-device-a-consona-username-init)).

```sh
consona setup
```

**What each part means:** as in 1.1. Without GitHub, setup routes only
this device's scopes: your consona username, the scopes of registered
folders and of ssh remotes.

**What you'll see / what happens:**

```
consona: this device's scopes (username, registered folders, ssh remotes): @alice
consona: GitHub not available (cannot read your GitHub account (GET /user): no GitHub credential available.
  ...): serving this device's folders and ssh remotes only
...
consona: npm now asks http://127.0.0.1:48731/npm/ first (/home/alice/.npmrc): @alice served by consona (this device's folders, ssh remotes, GitHub), everything else goes to your previous registry
```

If there is no GitHub access, no `--scope` and no local scope either,
setup stops with the credential error instead (there would be nothing to
serve).

### 1.4 Undo / uninstall

**When to use:** you want npm back exactly as it was before consona.

**Setup / assumptions:** a machine where `consona setup` was run.

```sh
consona setup --undo               # same as: consona uninstall
```

**What each part means:**

- `setup --undo` – keywords: removes consona's block from your npmrc,
  removes auto-start and stops the daemon.
- `uninstall` – keyword alias for `setup --undo`.

**What you'll see / what happens:**

```
consona: removing consona's lines from the npm config
consona: npmrc /home/alice/.npmrc:
consona:   - # >>> consona: npm asks the local consona daemon first (managed by `consona setup`, undo: `consona setup --undo`) >>>
consona:   - registry=http://127.0.0.1:48731/npm/
...
consona:   - # <<< consona <<<
consona: removing auto-start
consona: stopping the daemon on 127.0.0.1:48731
consona: removed consona settings from /home/alice/.npmrc (npm uses your previous registry again), removed auto-start, stopped the daemon
```

Your registered folders, remotes, named tokens and logs stay in the
[config directory](../README.md#config-directory); an npm login you made while
consona was set up is kept. Delete the `consona` binary afterwards if you
want it gone completely.

## 2. Private GitHub packages

### 2.1 Install a private repo as a package

**When to use:** you have a private repo `github.com/your-github-name/your-lib`
with a `package.json` and want to use it in another project.

**Setup / assumptions:** your laptop, after [1.1](#11-set-up-consona-once-on-a-machine);
the project is in `~/projects/my-app`; your GitHub credential can read
`your-lib`. The repo needs no changes (no publishing, no registry config).

```sh
cd ~/projects/my-app
npm install @your-github-name/your-lib        # plain npm works
consona install @your-github-name/your-lib    # same, plus prefetching and a summary of where each package came from
```

**What each part means:**

- `@your-github-name/your-lib` – **your choice**: `@<GitHub owner>/<repo name>`.
  The owner (user or organisation) becomes the npm scope, the repo name
  becomes the package name. - `npm install` – npm itself; consona answers for routed scopes.
- `consona install` – keyword: runs `npm install` with the same arguments
  (starting the daemon first if needed) and prints consona's view of it.

**What you'll see / what happens:** the package lands in `node_modules`
and `package.json` / `package-lock.json` like any npm package, with a real
`integrity` hash. `consona install` ends with lines like:

```
consona: source: @your-github-name/your-lib@1.4.0 <- github github.com/your-github-name/your-lib
```

### 2.2 Versions: git tags vs untagged repos

**When to use:** you want to know which versions npm sees for a repo.

**Setup / assumptions:** as 2.1.

```sh
npm view @your-github-name/your-lib versions    # list what consona offers
npm install @your-github-name/your-lib@^1.2.0   # normal semver ranges work
```

**What each part means:**

- Tags `v1.2.0` and `1.2.0` both become version `1.2.0` (a full semver is
  required; other tags are ignored, duplicates collapse). The version
  comes from the tag, not from `package.json`.
- A repo **without any semver tag** is served as a single version
  `0.0.0-<commit>` from its default branch, where `<commit>` is the first
  7 characters of the latest commit (e.g. `0.0.0-1282889`). A new commit
  gives a new `0.0.0-...` version.
- When a repo has semver tags, only tagged commits are versions; commits
  after the newest tag are not served until you tag them.

**What you'll see / what happens:** `npm view ... versions` lists e.g.
`[ '1.0.0', '1.2.0', '1.4.0' ]`, or `0.0.0-1282889` for an untagged repo.

### 2.3 Release a new version by pushing a tag

**When to use:** you changed `your-lib` and want projects to get it.

**Setup / assumptions:** the `your-lib` checkout on any machine with push
rights; the consuming project on your laptop.

```sh
# in the your-lib repo
git tag v1.5.0                     # v1.5.0 is your choice: any full semver, with or without "v"
git push origin v1.5.0             # this is the release; there is no npm publish step

# in ~/projects/my-app
npm install @your-github-name/your-lib@latest   # or: npm update @your-github-name/your-lib
```

**What each part means:** `git tag` / `git push` are plain git; `v1.5.0`
is the version you choose; `@latest` asks npm for the highest version.

**What you'll see / what happens:** the new version is visible to npm as
`1.5.0` and the lockfile records it. `npm publish` for a private scope is
refused on purpose (see [6.4](#64-publishing-public-works-private-is-refused)).

### 2.4 Packages that need a build (TypeScript etc.)

**When to use:** `your-lib` has a `prepare` or `prepack` script (for
example `"prepare": "tsc"`) and its build output is not committed.

**Setup / assumptions:** as 2.1; the build tools are the package's own
`devDependencies`; they run on **your** machine.

```sh
consona install @your-github-name/your-lib
```

**What each part means:** no extra flags. If `package.json` has `prepare`
or `prepack`, consona runs (like npm does for git dependencies)
`npm install --include=dev` in a copy of the repo, which runs `prepare`,
then `npm run prepack` if present, then `npm pack`.

**What you'll see / what happens:**

- The build runs **once per commit** and is cached; reinstalling the same
  version is instant.
- npm's first request for such a package waits for the latest version's
  build (up to `CONSONA_BUILD_WAIT`, default `90s`; `0` never waits) so
  the lockfile gets its `integrity`. If the build takes longer, the
  version is served without `dist.integrity` this time and a warning
  suggests raising `CONSONA_BUILD_WAIT`; a later install fills it in.
- The log shows `[build.start]` / `[build.finish] ... result=ok ms=...`.

### 2.5 When a build fails

**When to use:** an install fails with `build failed`.

**Setup / assumptions:** as 2.4, but `tsc` reports an error in `your-lib`.

```sh
consona install @your-github-name/your-lib
consona logs --event build --errors          # the same failures, later
```

**What each part means:** `logs` – keyword (section 7); `--event build`
– keyword filter for `build.start` / `build.finish`; `--errors` – only
error entries.

**What you'll see / what happens:** the error names the failing script
and shows the last 20 lines of its output, with credentials removed, and
where the full output is:

```
consona: build failed for @your-github-name/your-lib@1.5.0 (commit 3fa9c21): the package's "prepare" script failed (during npm install --include=dev); last lines of the build output:
src/index.ts(3,7): error TS2322: Type 'string' is not assignable to type 'number'.
...
full output: /home/alice/.cache/consona/.../build-failed.log
```

Fix the code, push (and tag) again, and reinstall.

### 2.6 A private package that depends on another private package

**When to use:** `your-lib` has `"@your-github-name/your-core": "^1.0.0"`
(or `@acme-co/...`) in its `dependencies`.

**Setup / assumptions:** as 2.1; both owners are routed (they are your
GitHub owners, or added with `--scope`) and a credential can read both
repos.

```sh
npm install @your-github-name/your-lib     # your-core comes along automatically
```

**What each part means:** nothing special to type. npm asks consona for
every `@your-github-name/...` and `@acme-co/...` name it meets, at any
depth, and consona resolves each one from GitHub with the credential
order for **that** owner. Builds use the same npm config, so a
`prepare` that needs another private package also works.

**What you'll see / what happens:** one `consona: source: ... <- github ...`
line per private package. If a credential cannot read one of them, the
error names that package and every credential tried (see [5.6](#56-when-no-credential-works)).

### 2.7 Several GitHub accounts or organisations (`--scope`)

**When to use:** you install packages from an owner that `consona setup`
did not find automatically (for example an organisation your token
cannot list, or a second account).

**Setup / assumptions:** your laptop; a credential for that owner exists
(for example a named token `work` for `@acme-co`, see [5.3](#53-named-tokens-add-list-test-remove)).

```sh
consona setup --scope @acme-co --scope @other-org   # repeat --scope for each owner
npm install @acme-co/billing-lib
```

**What each part means:**

- `--scope` – keyword; `@acme-co`, `@other-org` – **your choice**: GitHub
  owners whose packages consona should serve. `@acme-co` and `acme-co`
  mean the same. `--scope=@acme-co` also works.
- Each owner can use its own credential (`CONSONA_TOKEN_ACME_CO` or a
  named token `--for @acme-co`).

**What you'll see / what happens:** the npmrc gets one more
`@acme-co:registry=...` line. If GitHub owner discovery fails but you
passed `--scope`, setup warns and routes only the `--scope` owners.

### 2.8 Renamed or transferred repo, and package name mismatch warnings

**When to use:** `consona install` ends with `consona: warning:` lines.

**Setup / assumptions:** as 2.1.

```sh
consona install
consona logs --event source --level warn     # all such warnings, later
```

**What each part means:** `--event source` matches `source.redirect`,
`source.name_mismatch`, `source.tag_moved` (and `source.resolve` /
`source.fail`); `--level warn` shows warnings and above.

**What you'll see / what happens:**

- Repo renamed or moved to another owner (GitHub redirects the old name):

  ```
  consona: warning: github.com/your-github-name/old-name was renamed or transferred to github.com/acme-co/new-name; consona follows GitHub's redirect for now, but update the dependency to the new name before the old one is reused or the redirect goes away (@acme-co/new-name)
  ```

  The install still works; change the dependency to the new name.
- `package.json` inside the repo/folder names a different package than
  the one being served:

  ```
  consona: warning: @alice/date-utils@1.0.0-local.1.hcb1e1154: package.json in local folder /home/alice/Desktop/.../date-utils says its name is "date-utils"; consona serves it as @alice/date-utils (the name comes from the repo). Code that imports "date-utils" will not find it; rename the package or the repo so they match
  ```

  The package installs under the served name; make `package.json`'s
  `name` match it.

### 2.9 A tag was moved after you installed it

**When to use:** someone re-pointed a tag (`git tag -f v1.2.0 && git push -f`)
and a lockfile still has the old build's integrity.

**Setup / assumptions:** as 2.1, the project's lockfile has `1.2.0`.

```sh
consona install
npm install @your-github-name/your-lib@1.2.0     # what the warning tells you to run
```

**What you'll see / what happens:**

```
consona: warning: @your-github-name/your-lib@1.2.0: tag v1.2.0 now points at commit 9b1d2e4, but this version was built here before from commit 3fa9c21; a lockfile written then has the old integrity and npm will fail with EINTEGRITY. Run `npm install @your-github-name/your-lib@1.2.0` to update the lockfile, or move the tag back
```

### 2.10 Serve a folder of a repo as a package (`consona map add`)

**When to use:** one private repo holds several packages (a monorepo)
and you want to install one folder of it by name, without splitting the
repo or publishing anything.

**Setup / assumptions:** as 2.1; the repo `acme-co/widgets` has
`packages/core/package.json` (its `name` is `@acme-co/widgets-core`) and
`packages/ui/package.json` (its `name` is `@acme-co/widgets-ui`), and
your GitHub credential can read the repo.

```sh
consona map add @acme-co/widgets-core acme-co/widgets packages/core
consona map add @acme-co/widgets-ui   acme-co/widgets packages/ui
cd ~/projects/my-app
npm install @acme-co/widgets-core     # pnpm works the same way
```

**What each part means:**

- `map add` – keyword. Its three arguments are **your choice**: the
  package name to serve, the `owner/repo` on GitHub, and the folder
  inside the repo (slash-separated, no leading slash; the repo root,
  `.` and `..` are refused). The name is lowercased.
- The mapping lives on this device only (`folder-map.json` in consona's
  config directory, mode 0600, like the other config files). The scope
  is routed in your npmrc right away when npm is set up, and
  `consona setup` includes mapped scopes like any other private scope.

**What you'll see / what happens:**

```
consona: mapped @acme-co/widgets-core to folder packages/core of github.com/acme-co/widgets (its versions are the repo's tags)
consona: source: @acme-co/widgets-core@1.4.0 <- github-folder github.com/acme-co/widgets (folder packages/core)
```

`consona which @acme-co/widgets-core` shows the same source line. A
mapped name wins over ssh remotes and a same-named GitHub repo; a
registered local folder still wins over the map (see 3.9 and 4.6). If
the repo is unreadable or the folder does not exist at that commit, the
install fails naming the repo and folder; a mapping never falls through
to a same-named repo.

### 2.11 Map everything a repo declares (`consona map sync`)

**When to use:** the repo already lists the packages it serves, so each
device maps them all with one command instead of one `map add` each.

**Setup / assumptions:** as 2.10; `acme-co/widgets` keeps this
`consona.packages.json` at its root:

```json
{"packages":[{"name":"@acme-co/widgets-core","path":"packages/core"},
             {"name":"@acme-co/widgets-ui","path":"packages/ui"}]}
```

```sh
consona map sync acme-co/widgets
consona map list
consona map remove @acme-co/widgets-ui
```

**What each part means:**

- `map sync` – keyword; `acme-co/widgets` – **your choice**. Reads the
  repo's root `consona.packages.json` at its default branch and makes
  this device's map match it: new declarations are added, changed ones
  updated, and names the repo no longer declares are unmapped. Entries
  you added by hand (`map add`) are kept, unless the repo declares the
  same name - the repo's declaration then replaces them.
- `map list` – keyword: what this device maps, and how each entry got
  there (`map add` or `map sync`).
- `map remove` – keyword; fails when the name is not mapped.

**What you'll see / what happens:**

```
consona: synced acme-co/widgets: 2 package(s) declared
NAME                     REPO            FOLDER         HOW
@acme-co/widgets-core    acme-co/widgets packages/core  map sync
@acme-co/widgets-ui      acme-co/widgets packages/ui    map sync
```

If the repo cannot be read or the file is missing or invalid, sync
fails and the map is left unchanged.

### 2.12 Monorepo versions and sibling dependencies

**When to use:** mapped packages depend on each other, or you want to
know which versions npm sees for a mapped folder.

**Setup / assumptions:** as 2.10; `packages/core/package.json` declares
`"@acme-co/widgets-ui": "^1.0.0"` in its `dependencies`.

```sh
npm view @acme-co/widgets-core versions     # what consona offers for the folder
npm install @acme-co/widgets-core           # widgets-ui comes along automatically
```

**What each part means / what happens:**

- Versions belong to the repo, not the folder: tagging `acme-co/widgets`
  `v1.4.0` offers both folders as `1.4.0` (see 2.2). Per-folder versions
  are not supported.
- An untagged repo serves a single `0.0.0-<commit>` prerelease, and a
  dependency range must admit what the repo serves. On an untagged repo
  `^1.0.0` matches nothing and npm fails with
  `ETARGET No matching version found`; a range like `>=0.0.0-0` admits
  the prerelease. Tag the repo `v1.x` and `^1.0.0` works. This is a
  real failure to plan for, not a warning: existing `^` ranges between
  the repo's packages need matching tags before they install.
- Each package resolves on its own. Two mapped folders of one repo
  normally install at the same commit, but if the repo head moves
  between the two resolutions they can pin different commits - one
  install is not an atomic snapshot across packages. For a reproducible
  sibling set, tag the repo and depend on the tagged versions.
- The lockfile pins each version with its integrity. A moved tag (see
  2.9) or a remap to another folder changes the bytes, and npm then
  fails with `EINTEGRITY`: the change is detected, but the old bytes are
  not fetched again - update the lockfile or move the tag back.

### 2.13 What a mapped folder sees at install and build time

**When to use:** a mapped package runs a build or imports files, and you
want to know exactly what is present.

**Setup / assumptions:** as 2.10.

**What you'll see / what happens:**

- Only the mapped folder is extracted, and it builds exactly as if it
  were a repo of its own (2.4): its `package.json` must sit at the
  folder root, and files outside the folder - root configs, a shared
  tsconfig, `file:` dependencies pointing outside - are not present at
  install or build time. Keep each package self-contained in its folder.
  A folder without a `package.json` fails the install, naming the repo,
  folder and commit.
- The served name and version come from the map and the repo's tags, not
  from the folder's `package.json`. Make its `name` match the mapped
  name; otherwise the name-mismatch warning of 2.8 applies and code
  importing the other name will not find it.
- The whole commit tarball is downloaded before the folder is extracted:
  the map chooses what is served, it is not access control. A credential
  that can read the repo can read every folder in it.
- Remapping a name to another folder (or another repo) rebuilds instead
  of serving the previous folder's cached bytes; both mappings stay
  cached under their own identities, no cache clearing needed.

## 3. Local packages on the same machine

### 3.1 Give this device a consona username (`init`)

**When to use:** once per device, before `register` or remotes.

**Setup / assumptions:** your laptop. The username is **not** a GitHub or
npm account; it is only the default npm scope for folders you register.

```sh
consona init --username alice      # alice = your choice; packages become @alice/<name>
consona whoami                     # show it again later
```

**What each part means:**

- `init` – keyword. Without `--username` it asks in the terminal.
- `--username` – keyword; `alice` – **your choice** (scope `@alice`).
- `--force` – keyword: needed to change the username later.
- A random **device id** is created automatically; installs show which
  device served a package.

**What you'll see / what happens:**

```
$ consona init --username alice
consona: initialised: username @alice, device 79f0fd6c-3f12-4737-8cf2-af462657934d (alice-laptop, linux/arm64)
consona: config /home/alice/.config/consona/identity.json
$ consona whoami
username: @alice
device:   79f0fd6c-3f12-4737-8cf2-af462657934d
host:     alice-laptop (linux/arm64)
created:  2026-10-26 18:14 UTC
config:   /home/alice/.config/consona
```

If npm is already set up for consona, `@alice` is routed in your npmrc
right away.

### 3.2 Register a deep folder and use it from a project elsewhere

**When to use:** a library lives in one place (e.g. on the Desktop) and a
project somewhere else (e.g. in Documents) should install it, without
relative paths, `npm link` or publishing.

**Setup / assumptions:** your laptop, after 3.1 and [1.1](#11-set-up-consona-once-on-a-machine).
Library: `~/Desktop/projects/2026/client-x/libs/date-utils` with a
`package.json` (`"name": "@alice/date-utils", "version": "1.0.0"`).
Project: `~/Documents/apps/my-app`.

```sh
consona register ~/Desktop/projects/2026/client-x/libs/date-utils   # serve this folder as @alice/date-utils
cd ~/Documents/apps/my-app
consona install @alice/date-utils                                   # or: npm install @alice/date-utils
```

**What each part means:**

- `register` – keyword; the path – **your choice**: any folder with a
  `package.json` (relative paths and symlinks are resolved).
- The name is `@<your username>/<package.json name without its scope>`,
  here `@alice/date-utils`, unless you pass `--name` (3.4).
- `@alice/date-utils` in the install is that name; the two folders do not
  need to be related.

**What you'll see / what happens:**

```
$ consona register ~/Desktop/projects/2026/client-x/libs/date-utils
consona: registered @alice/date-utils -> /home/alice/Desktop/projects/2026/client-x/libs/date-utils (device alice-laptop (79f0fd6c))
consona: current version 1.0.0-local.1.hcb1e1154 (changes to the folder publish a new version on the next install)

$ consona install @alice/date-utils
consona: npm is set up for consona (/home/alice/.npmrc)
consona: daemon healthy on 127.0.0.1:48731
consona: running: npm install @alice/date-utils --verbose (private scopes via http://127.0.0.1:48731/npm/, everything else via https://registry.npmjs.org)
added 1 package, and audited 2 packages in 604ms
consona: [build.finish] INFO pkg=@alice/date-utils ... version=1.0.0-local.1.hcb1e1154 ... result=ok ms=650
consona: npm install exited with code 0 after 865ms
consona: source: @alice/date-utils@1.0.0-local.1.hcb1e1154 <- local /home/alice/Desktop/projects/2026/client-x/libs/date-utils [device alice-laptop (79f0fd6c)]
```

The version is `<package.json version>-local.<n>.h<content hash>`. Your
folder is never written to (no `node_modules`, lockfile or build output
left behind). If `package.json` has a different name (e.g. plain
`"date-utils"`), it is still served as `@alice/date-utils` with a note
and a name-mismatch warning ([2.8](#28-renamed-or-transferred-repo-and-package-name-mismatch-warnings)).

### 3.3 Pick up changes from a live folder

**When to use:** you edited `date-utils` and want `my-app` to get the new
code.

**Setup / assumptions:** as 3.2; `my-app` already has `@alice/date-utils`
in its lockfile.

```sh
# edit ~/Desktop/projects/2026/client-x/libs/date-utils/index.js, then:
cd ~/Documents/apps/my-app
consona install                    # no arguments: notices the changed folder and updates it
```

**What each part means:** a registered folder is always served in its
**current** state; every content change becomes a higher version
(`-local.2...`, `-local.3...`). `consona install` with no package
arguments runs `npm update <name>` for registered folders that changed.

**What you'll see / what happens:**

```
up to date, audited 2 packages in 316ms
consona: npm install exited with code 0 after 553ms
consona: local folder changed for @alice/date-utils: npm update
changed 1 package, and audited 2 packages in 852ms
consona: source: @alice/date-utils@1.0.0-local.2.h13f4eec9 <- local /home/alice/Desktop/projects/2026/client-x/libs/date-utils [device alice-laptop (79f0fd6c)]
```

Plain `npm install` keeps the lockfile version (normal npm behaviour);
use `npm update @alice/date-utils` or `consona install @alice/date-utils@latest`
there. An unchanged folder is not rebuilt.

### 3.4 Choose the package name (`--name`)

**When to use:** the folder's `package.json` name is not what you want to
install, or it has no name.

**Setup / assumptions:** as 3.2; folder `~/code/helpers`.

```sh
consona register ~/code/helpers --name @alice/string-helpers
consona register ~/code/helpers2 --name string-helpers-2    # no scope: becomes @alice/string-helpers-2
```

**What each part means:** `--name` – keyword; `@alice/string-helpers` –
**your choice**. A name in a scope that is already private (your
username, your GitHub owners, scopes you registered before) is accepted;
any other scope needs `--allow-scope` (3.6).

**What you'll see / what happens:** `consona: registered @alice/string-helpers -> /home/alice/code/helpers ...`
and, when `package.json` says something else, a note that the packed
`package.json` is renamed (your folder is not touched).

### 3.5 Move or rename a registration (`--replace`)

**When to use:** you moved the library to a new folder, or want to give
the same folder another name.

**Setup / assumptions:** `@alice/date-utils` is registered (3.2).

```sh
consona register ~/code/date-utils-v2 --name @alice/date-utils --replace
```

**What each part means:** `--replace` – keyword. One folder has one name
and one name has one folder per device; without `--replace` a second
registration of the same name or folder is refused.

**What you'll see / what happens:**

```
consona: replaced @alice/date-utils -> /home/alice/Desktop/projects/2026/client-x/libs/date-utils
consona: registered @alice/date-utils -> /home/alice/code/date-utils-v2 (device alice-laptop (79f0fd6c))
```

### 3.6 Register into a scope that is not private yet (`--allow-scope`)

**When to use:** rarely: you deliberately want a local folder to answer
for a scope that is otherwise public, for example your own fork of a
`@types/...` package on this device.

**Setup / assumptions:** as 3.2.

```sh
consona register ~/code/my-types --name @types/date-utils                  # refused
consona register ~/code/my-types --name @types/date-utils --allow-scope    # you really mean it
```

**What each part means:** `--allow-scope` – keyword. After this, consona
treats the **whole** `@types` scope as private on this device, so other
`@types/...` packages would no longer come from the public registry.

**What you'll see / what happens:**

```
consona: @types/date-utils is outside your scope @alice and @types is not a private scope yet; registering it would hide public @types packages on this device. Pass --allow-scope if that is intended
```

### 3.7 See what is registered and where a name comes from

**When to use:** to check what this device serves, or why a name resolves
the way it does.

**Setup / assumptions:** as 3.2.

```sh
consona registered                 # every folder this device serves
consona which @alice/date-utils    # where this name would come from right now
```

**What each part means:** `registered` and `which` – keywords;
`@alice/date-utils` – the package name to look up (any private-scope
name, also GitHub or remote ones).

**What you'll see / what happens:**

```
$ consona registered
NAME               VERSION                  FOLDER                                                     DEVICE
@alice/date-utils  1.0.0-local.1.hcb1e1154  /home/alice/Desktop/projects/2026/client-x/libs/date-utils  this device

$ consona which @alice/date-utils
@alice/date-utils@1.0.0-local.1.hcb1e1154 <- local /home/alice/Desktop/projects/2026/client-x/libs/date-utils [device alice-laptop (79f0fd6c)] [0s]
```

For a name that is nowhere, `which` lists every place it looked (this
device's folders, ssh remotes, every GitHub credential source).

### 3.8 Stop serving a folder (`unregister`)

**When to use:** you no longer want the folder served.

```sh
consona unregister @alice/date-utils
```

**What each part means:** `unregister` – keyword; the argument is the
package **name**, not the folder path.

**What you'll see / what happens:**

```
consona: unregistered @alice/date-utils (folder /home/alice/Desktop/projects/2026/client-x/libs/date-utils left as is)
```

Projects that already installed it keep their copy in `node_modules`.

### 3.9 A local folder wins over the GitHub repo

**When to use:** you have a checkout of a GitHub package
(`@your-github-name/your-lib`) and want projects on this machine to use
your local, unpushed changes.

**Setup / assumptions:** your laptop; checkout at `~/code/your-lib`.

```sh
consona register ~/code/your-lib --name @your-github-name/your-lib
consona which @your-github-name/your-lib
consona install @your-github-name/your-lib@latest
# later, back to GitHub:
consona unregister @your-github-name/your-lib
npm install @your-github-name/your-lib@latest
```

**What each part means:** `@your-github-name` is already a private scope
(your GitHub owner), so no `--allow-scope` is needed.

**What you'll see / what happens:**

```
consona: note: @your-github-name/your-lib also exists on GitHub (github.com/your-github-name/your-lib); the local folder wins on this device
```

`which` and install then show `<- local ...`. This only affects this
device. If the local folder is broken (bad `package.json`, failing
build), you get that error; consona does not silently fall back to
GitHub.

## 4. Packages on other machines over SSH

A **remote** is another machine you can already `ssh` into **that has
consona installed and folders registered** (`consona init` +
`consona register` run **on that machine**). Its registered packages
install on your machine over ssh; the build runs on the remote and the
tarball is streamed back and checked. No token is ever sent over ssh.

### 4.1 Prepare the other machine

**When to use:** before adding a machine as a remote.

**Setup / assumptions:** the other machine is reachable as
`alice@203.0.113.10`; `ssh alice@203.0.113.10 true` works from your laptop
without a password prompt (key or agent). The library there is
`/home/alice/src/report-kit`.

```sh
# on the OTHER machine (203.0.113.10):
consona init --username alice
consona register ~/src/report-kit          # served as @alice/report-kit
consona registered
```

**What each part means:** the same commands as section 3, run on the
other machine. consona must be on the non-interactive ssh PATH there
(consona also looks in `~/.local/bin` and nvm), or you pass
`--remote-cmd` (4.3).

### 4.2 Add a remote and install from it

**When to use:** your laptop should install packages registered on the
other machine.

**Setup / assumptions:** your laptop, after 1.1; the other machine is
prepared (4.1).

```sh
consona remote add work-laptop alice@203.0.113.10   # work-laptop = a nickname you choose
consona install @alice/report-kit
```

**What each part means:**

- `remote add` – keywords.
- `work-laptop` – **your choice**: a short nickname used in consona's
  output and in `remote test/remove`. It does not have to match the
  machine's hostname.
- `alice@203.0.113.10` – **your choice**: what you would type after
  `ssh`: `user@host`, or a `Host` alias from `~/.ssh/config` (4.4).
- `remote add` tests the connection right away; on success the remote's
  scopes are routed in your npmrc.

**What you'll see / what happens:**

```
consona: added remote work-laptop (work-laptop (alice@203.0.113.10))
consona: remote work-laptop (work-laptop (alice@203.0.113.10)): ok: @alice device alice-desktop (248cea86), 1 package(s): @alice/report-kit [612ms]
...
consona: source: @alice/report-kit@0.1.0-local.1.h201c567f <- ssh work-laptop (alice@203.0.113.10) [device alice-desktop (248cea86)]
```

If the machine cannot be reached, the remote is still saved and the test
reports the error (exit code 1):

```
consona: remote work-laptop (work-laptop (alice@203.0.113.10)): error: remote work-laptop (alice@203.0.113.10): ssh failed: ssh: connect to host 203.0.113.10 port 22: Connection timed out [3.012s]
```

Remote packages are not auto-updated by `consona install`; run
`npm update @alice/report-kit` after changes on the other machine.

### 4.3 Remote options: port, key, consona path, replace

**When to use:** ssh needs a non-default port or key, consona is not on
the remote's PATH, or you want to change an existing remote.

**Setup / assumptions:** as 4.2; sshd listens on port 2222; the key is
`~/.ssh/id_ed25519`; consona on the remote is `/opt/consona/bin/consona`.

```sh
consona remote add work-laptop alice@203.0.113.10 \
  --port 2222 \
  --identity ~/.ssh/id_ed25519 \
  --remote-cmd /opt/consona/bin/consona
consona remote add work-laptop alice@203.0.113.10 --replace   # change it later (here: back to defaults)
```

**What each part means:**

- `--port 2222` – keyword + your value: ssh port (1-65535).
- `--identity ~/.ssh/id_ed25519` – keyword + your value: private key file
  for ssh (`~` is expanded; stored as an absolute path).
- `--remote-cmd /opt/consona/bin/consona` – keyword + your value: path of
  consona **on the remote** (`CONSONA_REMOTE_CMD` sets a default).
- `--replace` – keyword: overwrite the remote with the same name.
  Without it you get:
  `consona: remote "work-laptop" already exists (work-laptop (alice@203.0.113.10:2222)); use --replace to change it`
- `--no-test` – keyword: save without the connection test.

### 4.4 Use a host from `~/.ssh/config`

**When to use:** you already have the machine in your ssh config.

**Setup / assumptions:** `~/.ssh/config` on your laptop contains:

```
Host work-laptop
  HostName 203.0.113.10
  User alice
  Port 2222
  IdentityFile ~/.ssh/id_ed25519
```

```sh
consona remote add work-laptop work-laptop    # first = nickname, second = the ssh alias
# or add nothing at all: concrete Host entries are tried automatically
consona remote list
```

**What each part means:** the second argument is the `Host` alias, so
ssh takes port, user and key from your config. Even without
`remote add`, every concrete `Host` in `~/.ssh/config` (and its
`Include`s) is tried **after** explicit remotes; wildcard hosts, git
forges (github.com, gitlab.com, ...) and `User git` hosts are skipped.
`CONSONA_SSH_AUTODETECT=0` turns this off. For an auto-detected host,
route its scope once: `consona remote test` (it routes the scopes it
finds on reachable remotes), `consona setup --scope @alice`, or add the
host explicitly with `remote add`.

**What you'll see / what happens:** in `remote list`, KIND is `explicit`
for added remotes and `ssh-config` for detected ones.

### 4.5 List, test and remove remotes

```sh
consona remote list                # NAME, TARGET, KIND, LAST CHECK, STATUS
consona remote test                # test every remote (explicit and ssh-config)
consona remote test work-laptop    # test one
consona remote remove work-laptop  # forget it (nothing changes on the other machine)
```

**What each part means:** `remote list|test|remove` – keywords (`ls`,
`rm` also work); `work-laptop` – the nickname from `remote add`.

**What you'll see / what happens:**

```
$ consona remote list
NAME         TARGET                            KIND      LAST CHECK          STATUS
work-laptop  work-laptop (alice@203.0.113.10)  explicit  Oct 26 23:44 IST  ok: @alice device alice-desktop (248cea86), 1 package(s): @alice/report-kit
$ consona remote remove work-laptop
consona: removed remote work-laptop
```

### 4.6 Which source wins

For a name in a private scope consona looks, in order:

1. **This device's registered folders** (`consona register`).
2. **SSH remotes**: explicit ones (`remote add`) first, then
   `~/.ssh/config` hosts. A down host costs one 3 s timeout and is then
   skipped for 5 minutes.
3. **GitHub** (asked in parallel with remotes, used if no remote has the name).
4. Nothing found: the error lists every place looked at.

```sh
consona which @alice/report-kit    # shows the winner: <- local / <- ssh / <- github
```

Names in scopes that are not private never reach this lookup; they go to
your previous registry.

## 5. GitHub credentials in practice

### 5.1 The order consona tries

For a package from GitHub owner `<owner>`: (1) `CONSONA_TOKEN_<OWNER>`,
(2) named tokens added `--for @<owner>`, (3) `CONSONA_TOKEN`,
(4) `GH_TOKEN`, then `GITHUB_TOKEN`, (5) the default named token,
(6) every gh account (the one whose login is the owner first, then the one
remembered for the owner, then the active one, then the rest; gh's active
account is never changed). A credential that gets 401, 403/404 or a rate limit is
skipped and the next one tried; the one that worked is used first for
that owner afterwards. Full details: [GitHub credentials](../README.md#github-credentials).

### 5.2 A token for one owner only (`CONSONA_TOKEN_<OWNER>`)

**When to use:** one organisation needs a different token than the rest.

**Setup / assumptions:** your laptop; a fine-grained token for `acme-co`
with Contents: Read-only, kept in your password manager.

```sh
export CONSONA_TOKEN_ACME_CO="<paste the acme-co token here>"   # variable name: owner upper-cased, '-' -> '_'
consona setup --no-autostart       # env vars are only seen by a daemon started from this shell
consona install @acme-co/billing-lib
```

**What each part means:** `CONSONA_TOKEN_` – keyword prefix; `ACME_CO` –
the owner `acme-co`, upper-cased with `-` turned into `_`. The token
value is yours; never commit it.

**What you'll see / what happens:** this token is tried first for
`@acme-co/...` only. The auto-started service does not see shell
variables, hence `--no-autostart` (setup prints a note if it sees such
variables with auto-start on).

### 5.3 Named tokens: add, list, test, remove

**When to use:** you want tokens stored in the OS secure store (macOS
Keychain, Windows Credential Manager, Linux Secret Service) instead of
environment variables; works with the auto-started daemon.

**Setup / assumptions:** your laptop with a secure store.

```sh
consona token add work --for @acme-co --description "Acme, read-only"   # prompts for the token (hidden)
printf '%s' "$TOKEN" | consona token add ci --default                   # or pipe it on stdin
consona token list
consona token test work
consona token remove work
```

**What each part means:**

- `token add|list|test|remove` – keywords.
- `work`, `ci` – **your choice**: token names (unique; `--replace`
  overwrites one).
- `--for @acme-co` – keyword + owner(s) this token is for (repeatable).
- `--description "..."` – keyword + your note.
- `--default` – keyword: the token used for any owner (step 5 of the order).
- `--no-verify` – keyword: store without checking it against GitHub.
- The token is never a command-line argument (`--token=...` is refused).

**What you'll see / what happens:** `token add` / `token test` print the
type, account, expiry, scopes, rate limit and PASS/FAIL checks (example
in [Named tokens](../README.md#named-tokens)). `token list` shows names and
metadata, never values:

```
NAME  FOR       LOGIN             TYPE          EXPIRES     ADDED       DESCRIPTION
work  @acme-co  your-github-name  fine-grained  2027-01-31  2026-10-26  Acme, read-only

index:   /home/alice/.config/consona/tokens.json
secrets: OS secure store, service dev.consona.tokens
```

The default token shows as `ci (default)`; EXPIRES is `never`, a date,
or `<date> EXPIRED`.

With none: `no named tokens (add one with `consona token add <name> --for @owner`)`.
Without a secure store (headless Linux), `token add` fails and says so.

### 5.4 Use what you already have: `GH_TOKEN`, `GITHUB_TOKEN` or `gh`

**When to use:** you already use the GitHub CLI or a token variable.

```sh
gh auth login                      # consona reads every gh account; nothing else to do
# or
export GH_TOKEN="<token>"          # or GITHUB_TOKEN
```

**What happens:** any one of them is enough; gh is optional. The
auto-started daemon uses the gh accounts and named tokens (not shell
variables). With several gh accounts (`gh auth login` again, gh 2.40+),
consona picks the one whose login is the package owner, or the one it
probed and remembered for an organisation, without switching gh's active
account:

```
$ gh auth status 2>&1 | grep account     # two accounts, your-other-account active
  ✓ Logged in to github.com account your-other-account (keyring)
  ✓ Logged in to github.com account your-github-name (keyring)
$ consona install
...
consona: @your-github-name/utils via gh account your-github-name (owner match)
```

### 5.5 CI and env-var-only machines

**When to use:** a CI job or server without a secure store or gh.

**Setup / assumptions:** the CI provides a secret with read access to
your repos as an environment variable.

```sh
export CONSONA_TOKEN="$CI_GITHUB_READ_TOKEN"   # or GH_TOKEN / CONSONA_TOKEN_<OWNER>; the name of the CI secret is your choice
consona setup --no-autostart       # no login service in CI
consona install                    # starts the daemon from this shell, then npm install
```

**What each part means:** `--no-autostart` – keyword: skip auto-start so
the daemon is started by `consona install` / `consona daemon start` and
inherits the variables. Add `--quiet` (or `CONSONA_QUIET=1`) for shorter
CI logs.

### 5.6 When no credential works

**What you'll see:** the error in consona's output, npm's error and the
daemon log lists every source tried, why it failed and how to fix it:

```
$ consona which @acme-co/billing-lib
consona: @acme-co/billing-lib: not found
  @acme-co/billing-lib: cannot read github.com/acme-co/billing-lib: no GitHub credential available.
  Not available: env CONSONA_TOKEN_ACME_CO, CONSONA_TOKEN, GH_TOKEN, GITHUB_TOKEN not set
    - named tokens for @acme-co: none added with --for @acme-co
    - default named token: none (consona token add --default)
    - gh login: gh is installed but not logged in (`gh auth login`)
  Fix: `consona token add <name> --for @acme-co` (token from https://github.com/settings/personal-access-tokens), or set CONSONA_TOKEN / CONSONA_TOKEN_ACME_CO / GH_TOKEN, or `gh auth login`.
  Also looked in: this device's registered folders: not registered
```

When credentials exist but are rejected, each one is listed with its
status (401, 404, SSO, rate limit, expiry) and a fix, as in
[GitHub credentials](../README.md#github-credentials).

## 6. Everyday npm with consona

### 6.1 Plain npm keeps working

**When to use:** always; consona does not replace npm.

```sh
npm install react                  # public: goes to your previous registry (normally registry.npmjs.org)
npm install @your-github-name/your-lib   # private: answered by consona
npm ci && npm test                 # lockfiles and scripts as usual
```

**What happens:** public packages are mostly passed on as a redirect, so
lockfiles keep the usual `registry.npmjs.org` addresses. Only routed
scopes (your GitHub owners, `--scope`, your consona username, registered
and remote scopes) are private.

### 6.2 `consona install` / `consona audit` vs plain npm

```sh
consona install                    # = npm install + daemon check + prefetch + "source:" summary + warnings
consona install --save-dev @your-github-name/your-lib   # any npm install arguments are passed on
consona i                          # short form
consona audit                      # = npm audit, starting the daemon if needed
```

**What each part means:** `install` / `i` / `audit` – keywords; the rest
is passed to npm. They check that npm is set up for consona, start the
daemon if needed, run npm with `--verbose` (unless quiet), and after an
install print where each private package came from and any warnings.
Plain `npm install` / `npm audit` work too, just without that summary.

### 6.3 `npm audit` never sends private names

```sh
npm audit                          # or: consona audit
```

**What you'll see / what happens:** private packages are removed from the
audit request; only public names go to the registry:

```
found 0 vulnerabilities
consona: [audit.filter] INFO result=ok audit: forwarding 0 public package names, withheld 1 private
```

### 6.4 Publishing: public works, private is refused

```sh
npm publish                        # an unscoped or public-scope package: goes to your registry with your npm login
npm publish                        # a package named @your-github-name/...: refused
```

**What you'll see / what happens** for a private name (npm's error):

```
403 Forbidden - PUT http://127.0.0.1:48731/npm/@alice%2fdate-utils - consona: @alice is a private scope served from GitHub; it is never published or changed on a public registry (release by pushing a git tag)
```

Release private packages with a git tag ([2.3](#23-release-a-new-version-by-pushing-a-tag)).

### 6.5 `npm login`, `npm whoami`, `npm logout`

```sh
npm login                          # logs in to your previous registry, through consona
npm whoami
npm logout
```

**What happens:** requests that need your registry login are passed on
with your existing login, found the way npm finds it (`npm_config_*`
variables, project `.npmrc`, `~/.npmrc`, global npmrc, `${NPM_TOKEN}`
references). consona never copies or logs the token; one-time passwords
(`--otp`) pass through; `setup --undo` keeps a login you made meanwhile.

### 6.6 The daemon is not running: `ECONNREFUSED`

**When to use:** npm fails with `ECONNREFUSED 127.0.0.1:48731`.

**Setup / assumptions:** consona is set up, but the daemon was stopped
(for example `consona daemon stop`, or `--no-autostart` after a reboot).

```sh
consona daemon start               # or any consona command that needs it, e.g. consona install
npm install                        # works again
```

**What you'll see / what happens:** npm stops within a few seconds with
something like:

```
npm error code ECONNREFUSED
npm error errno ECONNREFUSED
npm error FetchError: request to http://127.0.0.1:48731/npm/left-pad failed, reason: connect ECONNREFUSED 127.0.0.1:48731
```

npm never falls back to the public registry for anything, so private
names are not leaked. (npm may still answer some commands from its own
cache, e.g. `npm install` with an up-to-date `node_modules`.) Then:

```
$ consona daemon start
consona: daemon not running on 127.0.0.1:48731: starting it
consona: daemon up on 127.0.0.1:48731 in 18ms (log /home/alice/.cache/consona/daemon.log)
consona: daemon running on 127.0.0.1:48731 (log /home/alice/.cache/consona/daemon.log)
```

To stop routing npm through consona altogether, use `consona setup --undo`.

## 7. Visibility and troubleshooting

### 7.1 Live output, `--quiet` and `CONSONA_QUIET`

**When to use:** you want to see (or hide) what consona is doing.

```sh
consona install                    # default: consona's steps on stderr + npm --verbose
consona --quiet install            # consona silent, npm without --verbose (--quiet may go anywhere before --)
CONSONA_QUIET=1 consona install    # same, e.g. for CI (0/false/no/off = not quiet)
```

**What you'll see / what happens:** by default every command prints npm
config changes, the credential used, daemon health, live events like
`consona: [source.resolve] INFO ...`, `consona: source:` lines and
`consona: warning:` lines. With `--quiet` you only see npm's normal
output (`up to date, audited 2 packages in 307ms`) and errors.

### 7.2 npm's own verbosity (`loglevel=verbose`)

`consona setup` adds `loglevel=verbose` to its npmrc block **only if you
have no `loglevel` of your own**, so plain `npm` commands also show what
they fetch and from where. To change it, set your own level
(`npm config set loglevel warn`); `setup --undo` removes only the line
consona added.

### 7.3 Read the event log (`consona logs`)

**When to use:** "why did yesterday's install fail?", "where did this
package come from?".

```sh
consona logs --errors --since 2h                     # errors of the last 2 hours
consona logs --package @alice/date-utils --limit 20  # one package
consona logs --owner acme-co --level warn            # one GitHub owner, warnings and worse
consona logs --event credential --json               # credential.resolve / credential.fail as JSON
consona logs --event ssh.probe --errors              # remotes that could not be reached
consona logs --since 2026-10-20 --until 2026-10-21   # a date range
```

**What each part means:** filters (keywords): `--package @o/n`,
`--owner o`, `--event` (an event name or prefix: `build`, `install`,
`credential`, `source`, ...; full list in `consona logs help`),
`--level` (minimum level), `--errors`, `--since` / `--until` (`2h`,
`7d`, `2026-10-26`, RFC 3339), `--limit N`, `--json`.

**What you'll see / what happens:**

```
2026-10-26 18:14:47.416 [source.resolve] INFO pkg=@alice/date-utils owner=alice repo=date-utils version=1.0.0-local.1.hcb1e1154 result=ok local /home/alice/Desktop/projects/2026/client-x/libs/date-utils
```

No match prints `consona: no matching log entries`. Token values are
never stored.

### 7.4 Export, info, prune, clear

```sh
consona logs export --format jsonl -o consona-logs.jsonl --since 7d   # share or analyse (filters work here too)
consona logs info                  # backend, path, counts, retention
consona logs prune                 # apply retention now
consona logs clear                 # delete every entry (asks first); --yes skips the question
```

**What each part means:** `export|info|prune|clear` – keywords;
`--format jsonl|json`, `-o <file>` (your file name), `--yes`. Retention:
`CONSONA_LOG_MAX_AGE` (default 30d) and `CONSONA_LOG_MAX_SIZE_MB`
(default 50).

**What you'll see / what happens:**

```
$ consona logs info
backend:   sqlite
path:      /home/alice/.config/consona/logs/events.db
entries:   7
size:      48.1 KB
oldest:    2026-10-26T18:14:22Z
newest:    2026-10-26T18:14:22Z
retention: 720h0m0s, 50 MB
human log: /home/alice/.cache/consona/daemon.log
config:    /home/alice/.config/consona
```

### 7.5 Manage the daemon and check the version

```sh
consona daemon status              # check (starts it if it is down)
consona daemon start               # start it (no-op if running)
consona daemon stop                # stop it (npm then gets ECONNREFUSED until it runs again)
consona version
```

**What you'll see / what happens:** `consona: daemon running on 127.0.0.1:48731 (log ...)`,
`consona: daemon stopped`, `consona 0.10.0`. The daemon's one-line-per-request
log is the `daemon.log` path shown; `CONSONA_PORT` changes the port and
`CONSONA_HOME` the cache/log directory.

Use your private GitHub repos as npm packages, with no registry to run and
nothing to change in the repos. `npm install @your-github-name/your-repo`
just works for every repo your GitHub credentials can read. Everything else
(public packages, publishing, `npm login`) keeps working exactly as before.

Back to the [README](../README.md).
