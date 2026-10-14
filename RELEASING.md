# Building and releasing

The repo has two consumers of the same Go binary, and one source of truth.

```
*.go  ──┬── go build ──→ ~/.local/bin/mail-app-cli      (PATH; used by the agent skill)
        │
        ├── go build ──→ ghost/bin/mail-app-cli         (bundled by the Cindy plugin)
        │                        │
        │                        └── ghost/ ──→ .cindy  (ghost_forge_pack, or `make bundle`)
        │
        └── *.md ──→ skills/mail-app-cli/SKILL.md ──rsync──→ ~/.agents/skills/mail-app-cli/
```

## The trap: two copies of the same binary

`~/.local/bin/mail-app-cli` and `ghost/bin/mail-app-cli` are separate files that
both come from the same source. Build them with two separate commands and one of
them goes stale — silently, because `--version` is a hand-maintained constant in
`main.go` and does not track the commit.

`make plugin` avoids this by compiling **once** and installing the same bytes in
both places. `make verify` catches regressions.

Compare builds by embedded revision, not by hash (the file hash also depends on
`vcs.modified` and other build metadata):

```bash
go version -m ghost/bin/mail-app-cli   | grep vcs.revision
go version -m ~/.local/bin/mail-app-cli | grep vcs.revision
git rev-parse HEAD
```

## Everyday loop

```bash
make test              # go vet + go test
make plugin            # build once, install to ghost/bin/ and ~/.local/bin/
make verify            # pre-flight + "is the bundle built from HEAD"
```

Then install into Cindy, from the plugin source directory:

```
ghost_forge_install({ dir: "<repo>/ghost" })
```

Same `id` updates in place and keeps the enabled state, configuration, data and
panel position. Bump `version` in `ghost/ghost.json` when you want the change to
be visible as a new version; re-installing the same version is also allowed.

## Cutting a release

```bash
# 1. bump ghost/ghost.json "version" (and main.go's version if the CLI changed)
# 2. commit, then tag with a matching version
git tag v1.0.0 && git push origin v1.0.0
```

`.github/workflows/release.yml` then runs `go test`, builds the universal binary,
**checks the tag against `ghost/ghost.json`**, validates the package, produces the
`.cindy`, and attaches two assets to the GitHub Release:

| asset | what it is |
|---|---|
| `mail-app-cli-<version>.cindy` | the plugin package |
| `mail-app-cli-darwin-universal` | the standalone CLI (arm64 + x86_64) |

`.github/workflows/ci.yml` runs the same checks on every push, minus the release.

### What CI cannot do

`ghost_forge_pack` and `ghost_forge_install` are tools of the Cindy host
application, not a CLI — the only CLI Cindy ships
(`Cindy.app/Contents/Resources/cli/cindy`) is a launcher that does
`open -a Cindy.app -- <path>`. So **the authoritative package validation and the
install itself always happen on a machine running Cindy**, using the downloaded
artifact. CI is the gate and the distributor, not the installer.

Formal signing and review also belong to a release pipeline, not to a personal
repo: never put a publisher private key in this repository or in a chat.

### Installing from a release

The release `.cindy` is a plain zip of the package root, which is exactly what
`ghost_forge_install` wants once unpacked:

```bash
unzip mail-app-cli-1.0.0.cindy -d /tmp/mail-app-cli-1.0.0
```

```
ghost_forge_install({ dir: "/tmp/mail-app-cli-1.0.0" })
```

Dragging the `.cindy` into the Plugins page should work too — it is byte-for-byte
the same layout `ghost_forge_pack` emits — but only the directory path above is
verified here.

## Notes

- `make bundle` writes the same filename and location as `ghost_forge_pack`
  (`ghost/mail-app-cli-<version>.cindy`), so the two are interchangeable.
- `ghost/bin/` and `ghost/*.cindy` are gitignored: both are build output. A fresh
  clone therefore has no bundled binary — run `make plugin-universal` before
  packaging (CI does this for you).
- The bundled binary needs its executable bit set before packaging. `make plugin`
  and `make plugin-universal` do it, and `make verify` fails loudly if it is lost.
- `make verify` also **executes** the bundled binary (`--version`). That gate exists
  because CI once built a darwin/arm64 binary that dyld refused to load
  (`missing LC_UUID load command`) and nothing caught it: the other checks only
  inspect the file, they never run it. Never ship a binary that has not been run.
- The workflows pin `go-version` explicitly instead of using
  `go-version-file: go.mod`. The `go` directive in `go.mod` is the **minimum** Go
  the module supports; it is not the toolchain to build release artifacts with.
  Go 1.21 emits darwin/arm64 binaries without LC_UUID, which current macOS
  runners abort at load. Keep the pinned version at 1.22 or newer.
- `make skill` syncs `skills/mail-app-cli/` to `~/.agents/skills/mail-app-cli/`.
  `skills/mail-app-cli/SKILL.md` and `ghost/manual/operations/MANUAL.md` cover
  overlapping ground for two different audiences (any agent vs Cindy agents);
  changing CLI behaviour means reviewing both.
- First real call from the plugin pops a one-time macOS Automation prompt
  ("Cindy wants to control Mail"). That grant is separate from any grant given to
  Terminal.
