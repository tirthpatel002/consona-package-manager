# Consona rules

Rules for anyone who develops, publishes, consumes or automates around
packages served by consona - developers and AI agents alike. Each rule
states what to do and what consona actually does when you break it, so an
agent can reason about failures instead of guessing. Everything here
describes behavior the code and tests guarantee today; where something
is not guaranteed, the rule says so.

## 1. How a name resolves

R1. `npm install @owner/name` asks consona first for routed scopes.
Consona looks, in order: folders registered on this device
(`consona register`), the device folder map (`consona map`), ssh
remotes, then a GitHub repo `owner/name`. The first hit wins; a name
none of them has is a clear "not found" and is never asked on the
public registry.

R2. A name found in an earlier source never falls through to a later
one. A broken mapping (unreadable repo, missing folder) fails the
install with an error naming the repo and folder; it does not silently
serve a same-named repo. Treat that failure as "fix the mapping or the
repo", not "try another source".

## 2. Versions and tags

R3. Release by pushing a full semver tag: `v1.2.0` or `1.2.0` both
become version `1.2.0`. There is no publish step; `npm publish` of a
routed scope is refused on purpose. Tags in any other shape are
ignored.

R4. Never move or reuse a tag. New code gets a new tag. Consona warns
when a tag points at another commit than an earlier build of the same
version, and lockfiles are commit-pinned (R12), so a moved tag cannot
silently swap code - it surfaces as a warning and a URL change.

R5. A repo with no semver tag serves exactly one version:
`0.0.0-<commit>` of the default-branch head (a prerelease). This is
allowed and works, but normal ranges like `^1.0.0` do not match a
prerelease and fail with `ETARGET No matching version found`. Standard
practice for anything consumed by others: keep a release tag and
maintain it as the code changes.

R6. In a monorepo served through the folder map, a plain tag
(`v1.4.0`) is shared by every folder. To release one folder
independently, tag it by its full mapped name:
`@acme/widgets-core@v1.2.0` (the `v` is optional; matching ignores
case, and npm names are lowercase, so use the exact lowercase name).
A per-folder tag wins over a repo-wide tag of the same version. Repos
without per-folder tags behave exactly as before this convention
existed.

R7. The served name and version come from the source (repo, map or
registration) and its tags, not from `package.json`. Keep
`package.json`'s `name` equal to the served name: on a mismatch
consona warns and code importing the other name breaks.

## 3. Dependencies

R8. Depend on private packages by their normal scoped names; npm asks
consona for them at any depth. The range must admit what the source
actually serves (R5, R6): `^1.0.0` needs a matching `v1.x` tag;
`>=0.0.0-0` admits the untagged prerelease. An agent adding a
dependency must check the served versions (`npm view @owner/name
versions`) instead of inventing a range.

R9. Packages resolve one at a time: under npm's current registry
protocol each package is fetched independently and no install-session
identity reaches the registry, so consona cannot guarantee one atomic
snapshot across packages. If the repo head moves mid-install, two
packages can pin different commits. For a reproducible set, tag and
depend on tagged versions; the lockfile then pins exact commits. (An
explicit project-level pin or manifest could close this in future; it
is a protocol limitation, not a fundamental one.)

## 4. Folder maps (monorepos)

R10. Map with `consona map add @scope/pkg owner/repo packages/core`,
or declare the set in a root `consona.packages.json` and run
`consona map sync owner/repo`. Sync adds, updates and unmaps to match
the repo; if the repo or file is unreadable or invalid, sync fails and
the map is unchanged. The map lives on the device
(`folder-map.json`, mode 0600); it is not shared state.

R11. A mapped folder must be self-contained: `package.json` at the
folder root, and nothing the install or build needs outside the folder
(no root configs, shared tsconfig, or `file:` links leaving the
folder). Only the folder is extracted; a folder without `package.json`
fails the install, naming repo, folder and commit.

R12. The map is not access control. The whole commit tarball is
downloaded and only the folder is written to disk, so a credential
that can read the repo can read every folder in it. Do not put
secrets in a sibling folder "because it is not mapped".

## 5. Lockfiles, cache and recovery

R13. Commit the lockfile. Tarball URLs are commit-qualified, so it
pins version + integrity + the exact commit (and, for a mapped folder,
the mapping) each package was built from.

R14. After a tag moves or a version disappears, an old lockfile still
installs: consona serves the pinned commit's bytes from the build
cache, or rebuilds from that commit while it is reachable in the repo.
A rebuild matches the lockfile's integrity only when the package
builds reproducibly; if it does not, npm fails with `EINTEGRITY` -
that is the protection working, and the fix is a new tag, never a
moved one. The cache is a cache: it can be pruned, so recovery is not
permanent artifact retention.

R15. Remapping a name to another folder or repo gives it a new
identity. An old lockfile URL then serves the original cached bytes or
fails with a clean 404; it never serves the new folder's bytes under
the old identity. After an intentional remap, run
`npm install <name>@latest` to rewrite the lockfile.

R16. Builds run on the installing machine with the package's own
scripts, cached per commit and source. Build output shown in errors
and logs has credentials redacted; never work around a build failure
by disabling scripts or editing cached artifacts.

## 6. Credentials and security

R17. The installing machine needs read access to every repo a package
and its dependencies come from (per-owner `CONSONA_TOKEN_<OWNER>`,
named tokens, `GH_TOKEN`/`GITHUB_TOKEN`, or `gh`). Credential failures
name every credential tried and why each failed. Do not broaden a
token's scope to silence an error you have not read.

R18. Private names are never sent to the public registry, and the
lookalike guard (`consona check`, `CONSONA_STRICT=1`) blocks a public
package shadowing a private name. Do not route a private scope to the
public registry to make an install pass.

## 7. For AI agents specifically

R19. All rules above apply to agents exactly as to developers. In
addition: never invent versions, tags, mappings or credentials; check
what exists (`npm view`, `consona which`, `consona map list`,
`consona logs`) before acting. Never bypass integrity (`EINTEGRITY`),
never force a tag move, never weaken a dependency range to make an
install pass - report the mismatch and its cause instead. When a
failure contradicts this file, trust the observed failure and say so.

## 8. Known limits (current, honest)

- One install is not an atomic snapshot across packages under
  npm's current registry protocol (R9).
- Recovery of pinned commits depends on the build cache or a
  reproducible rebuild; neither is guaranteed forever (R14).
- The whole repo tarball is downloaded for a mapped folder (R12); very
  large repos pay that cost per new commit.
- Local folders registered with `consona register` are live by design:
  their versions move with the folder's content and are not
  commit-pinned.
- Verified locally with npm 10.9 and pnpm 12.15 on one machine and
  account. GitHub Actions CI and a second contributor's machine are
  not yet verified; treat those setups as unproven until run.
