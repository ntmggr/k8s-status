# Releasing

A release is one merge to `main`. Everything after that is automatic.

```
Conventional Commit  ->  tag.yml  ->  v1.2.3 tag  ->  release.yml  ->  image
                                                                   +  binaries
                                                                   +  checksums.txt
                                                                   +  GitHub release
```

## 1. The commit decides the version

`.github/workflows/tag.yml` runs on every push to `main` and reads the Conventional
Commit subjects since the last tag:

| Commit subject | Bump |
|---|---|
| `feat: ...` | minor |
| `fix: ...`, `perf: ...` | patch |
| anything with `BREAKING CHANGE` | major |
| `docs:`, `ci:`, `chore:`, `refactor:`, `test:` | none — no release at all |

If nothing release-worthy landed, no tag is created and the pipeline stops there.
Otherwise it pushes `vX.Y.Z`.

## 2. The tag triggers the release

`.github/workflows/release.yml` runs on `push` of a `v*.*.*` tag. In order:

1. **Resolve version** — strips the leading `v` and rejects anything that is not
   semver. The tag is the single source of truth for the image tag, the binary
   version and the GitHub release, so they cannot disagree.
2. **Test** — `go test ./... -race`. An image is never published from code that does
   not pass its own tests.
3. **Build and push the image** — buildx, `linux/amd64` and `linux/arm64`, tagged
   `X.Y.Z` plus `latest` (the `latest` tag is withheld from pre-release tags such as
   `v1.2.3-rc.1`).
4. **Chart version check** — warns, does not fail, if `charts/k8s-status/Chart.yaml`
   `appVersion` does not match the tag.
5. **goreleaser** — builds the binaries, packs the archives, writes `checksums.txt`
   and creates the GitHub release with all of it attached.

## 3. What goreleaser produces

`.goreleaser.yaml` builds six targets:

| OS | Architectures | Archive |
|---|---|---|
| linux | amd64, arm64 | `.tar.gz` |
| darwin | amd64, arm64 | `.tar.gz` |
| windows | amd64, arm64 | `.zip` |

Each archive contains the binary, `LICENSE` and `README.md`, and is named
`k8s-status_<version>_<os>_<arch>.<ext>`. `checksums.txt` holds a SHA-256 for every
archive:

```sh
sha256sum -c checksums.txt
```

Builds are `CGO_ENABLED=0` and `-trimpath`, with `mod_timestamp` pinned to the commit
timestamp so the archives do not change just because the clock moved.

The version is stamped with `-ldflags "-s -w -X main.version=<version>"` — the same
variable and the same flags the `Dockerfile` uses. That is deliberate: a binary
unpacked from an archive and the binary inside the image report the same version
string in the startup log and in the page footer.

## Two things that are deliberately not here

**goreleaser does not build the container image.** It has a `dockers:` feature and it
is not used. `release.yml` already builds and pushes the multi-arch image with buildx
and QEMU; adding a `dockers:` block would publish the same tag from two places in the
same run.

**Only goreleaser creates the GitHub release.** `softprops/action-gh-release` used to
do this and has been removed. Two steps creating the release for one tag race each
other, and only goreleaser has artifacts to attach. The image reference that step
used to put in the notes is now the goreleaser `release.header`, fed by the
`IMAGE_REPO` environment variable, so the notes still name the image that same run
pushed.

## Release notes

goreleaser derives the notes from the commit subjects, grouped into *Breaking
changes*, *Features*, *Fixes*, *Performance* and *Other*. `docs:`, `ci:`, `chore:`,
`test:` and merge commits are filtered out — they are already excluded from producing
a release, so they should not describe one either.

## Manual runs

`release.yml` also has a `workflow_dispatch` input that takes a bare version
(`1.2.3`). That path republishes the **image only**: goreleaser needs a real tag to
release against, so the goreleaser step is skipped. To publish binaries, push a tag.

## Checking the config locally

```sh
go install github.com/goreleaser/goreleaser/v2@latest

goreleaser check                       # validate .goreleaser.yaml
goreleaser build --snapshot --clean --single-target   # one target, fast
goreleaser release --snapshot --clean --skip=publish  # all six, archives + checksums
```

`--snapshot` never talks to GitHub and never pushes anything. Output lands in `dist/`,
which is gitignored.

## Do not create tags by hand

Pushing a `v*.*.*` tag publishes to Docker Hub and to the GitHub releases page for
real. Let `tag.yml` create tags from merged commits.
