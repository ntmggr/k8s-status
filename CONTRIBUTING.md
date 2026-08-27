# Contributing

Thanks for taking a look. Bug reports and pull requests are both welcome.

## Getting it running

You need Go and `kubectl`. Nothing else.

```sh
git clone https://github.com/ntmggr/k8s-status && cd k8s-status
./scripts/local-test.sh fixture              # no cluster needed
./scripts/local-test.sh cluster <context>    # against a real cluster, read-only
```

Then open http://127.0.0.1:8080/k8s-status/.

## Reporting a bug or asking for a feature

Open an issue. There are templates for both. For a bug, the useful things are what you
saw, what you expected, and whatever the page or `/api/status` reported at the time.

Security problems go to [SECURITY.md](SECURITY.md) instead, not to the issue tracker.

## What an acceptable change looks like

Before opening a pull request:

```sh
gofmt -l .          # must print nothing
go vet ./...
go test ./... -race
```

CI runs the same three, plus `helm lint`, an image build, Trivy, Dockle, govulncheck and
CodeQL. All of them have to pass.

Beyond that, four things this project holds to:

**No dependencies.** `go.mod` has no require block and the aim is to keep it that way.
The standard library has been enough so far, including for the Prometheus endpoint. If a
change seems to need a dependency, that is worth discussing in an issue first.

**No JavaScript.** The page is server-rendered HTML with no scripts. A test asserts there
are no `<script>` tags, so adding one will fail the build.

**Read-only.** The service uses `get` and `list` and nothing else. Any change that
introduces a write verb, or widens what the ServiceAccount can reach, needs a good reason
and should say so in the pull request.

**No installation-specific names.** This runs on other people's clusters. Examples in
code, tests and docs use generic names rather than services from any particular estate.

## Commits and releases

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org/),
because the release is cut from them:

- `feat:` gives a minor version
- `fix:` or `perf:` gives a patch version
- `docs:`, `ci:`, `chore:`, `refactor:` release nothing
- `BREAKING CHANGE:` in the body gives a major version

Merging to `main` tags and publishes on its own. There is no manual release step, so a
mislabelled commit either ships something unexpected or fails to ship at all.

## Tests

New behaviour needs a test. The ones worth writing here tend to be the ones that pin down
a decision rather than restate the code: what happens with an empty nodegroup, a workload
that reaches a device without requesting one, a cancelled request. Several of those were
written after a real bug and are commented with what went wrong, which is a good pattern
to follow.
