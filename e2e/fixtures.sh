#!/usr/bin/env bash
# Creates the private e2e fixture repos once (idempotent). No workflows,
# topics or releases: plain repos with a package.json, as a user would have.
#   lib-b: no deps, tag v1.0.0
#   lib-a: depends on lib-b, tag v1.0.0 plus one untagged commit after it
#   lib-c: no tags at all (default-branch fallback)
#   lib-ts: TypeScript source, `prepare` runs tsc to dist/, files: ["dist"], tag v1.0.0
set -euo pipefail
OWNER=${OWNER:-rakesh-jain-cb1998}
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT

mk() { # name tag files...
  local name=$1 tag=$2; shift 2
  if gh repo view "$OWNER/$name" >/dev/null 2>&1; then echo "exists: $OWNER/$name"; return; fi
  gh repo create "$OWNER/$name" --private --description "consona e2e fixture" >/dev/null
  local d=$W/$name; mkdir -p "$d"; cd "$d"; git init -q -b main
  git -c user.name="$OWNER" -c user.email="$OWNER@users.noreply.github.com" commit -q --allow-empty -m init
  "$@"
  git add -A
  git -c user.name="$OWNER" -c user.email="$OWNER@users.noreply.github.com" commit -q -m "package"
  [ -n "$tag" ] && git tag "$tag"
  if [ "$name" = consona-e2e-lib-a ]; then
    echo "untagged work in progress" > CHANGES.md; git add -A
    git -c user.name="$OWNER" -c user.email="$OWNER@users.noreply.github.com" commit -q -m "untagged commit"
  fi
  git remote add origin "https://github.com/$OWNER/$name.git"
  git push -q origin main --tags
  echo "created: $OWNER/$name"
}

lib_b() {
  cat > package.json <<J
{ "name": "@$OWNER/consona-e2e-lib-b", "version": "1.0.0", "main": "index.js", "types": "index.d.ts" }
J
  echo 'exports.shout = (s) => String(s).toUpperCase() + "!";' > index.js
  echo 'export declare function shout(s: string): string;' > index.d.ts
}
lib_a() {
  cat > package.json <<J
{ "name": "@$OWNER/consona-e2e-lib-a", "version": "1.0.0", "main": "index.js", "types": "index.d.ts",
  "files": ["index.js", "index.d.ts"],
  "dependencies": { "@$OWNER/consona-e2e-lib-b": "^1.0.0" } }
J
  echo "const { shout } = require('@$OWNER/consona-e2e-lib-b');
exports.greet = (name) => shout('hello ' + name);" > index.js
  echo 'export declare function greet(name: string): string;' > index.d.ts
  echo 'internal notes, excluded by "files"' > notes.txt
}
lib_c() {
  cat > package.json <<J
{ "name": "@$OWNER/consona-e2e-lib-c", "version": "0.1.0", "main": "index.js", "types": "index.d.ts" }
J
  echo 'exports.formatPrice = (n) => "$" + Number(n).toFixed(2);' > index.js
  echo 'export declare function formatPrice(n: number): string;' > index.d.ts
}

lib_ts() {
  cat > package.json <<J
{ "name": "@$OWNER/consona-e2e-lib-ts", "version": "1.0.0",
  "main": "dist/index.js", "types": "dist/index.d.ts", "files": ["dist"],
  "scripts": { "prepare": "tsc -p tsconfig.json" },
  "devDependencies": { "typescript": "^5.5.4" } }
J
  cat > tsconfig.json <<'J'
{ "compilerOptions": { "target": "ES2019", "module": "commonjs", "declaration": true,
  "outDir": "dist", "rootDir": "src", "strict": true }, "include": ["src"] }
J
  mkdir -p src
  cat > src/index.ts <<'T'
export interface SlugOptions { separator?: string }

export function slugify(input: string, opts: SlugOptions = {}): string {
  const sep: string = opts.separator ?? "-";
  return input.trim().toLowerCase().replace(/[^a-z0-9]+/g, sep).replace(new RegExp(`^${sep}|${sep}$`, "g"), "");
}
T
  printf 'node_modules/\ndist/\n' > .gitignore
}

mk consona-e2e-lib-b v1.0.0 lib_b
mk consona-e2e-lib-a v1.0.0 lib_a
mk consona-e2e-lib-c "" lib_c
mk consona-e2e-lib-ts v1.0.0 lib_ts
