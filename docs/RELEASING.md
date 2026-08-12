# Release process

InfernoSIM releases are produced from annotated `v*` tags by
`.github/workflows/release.yml`.

## Maintainer checklist

1. Confirm `go test -race ./...`, nested Testcontainers module tests,
   `go vet ./...`, `go mod tidy -diff`, the fuzz smoke suite, Docker builds,
   both Compose smoke profiles, `scripts/kafka-smoke.sh`, and the 100-run
   category benchmark pass.
2. Update `RELEASE_NOTES.md` and `docs/UPGRADING.md`.
3. Run `GH_PAT='' goreleaser check` and a local snapshot with `GH_PAT=''
   goreleaser release --snapshot --clean`. Snapshot mode generates the cask
   locally without publishing it.
4. Create and push an annotated version tag.
5. Verify GoReleaser uploaded exactly eight platform archives and
   `checksums.txt`—nine uploaded assets total. GitHub adds two source-code
   downloads automatically, so the release UI displays the established count
   of 11. Generated benchmark JSON, incident logs, reports, SBOMs, signature
   bundles, and GoReleaser's internal JSON/YAML metadata are not release
   assets.
6. Verify every archive against the checksum manifest:

   ```bash
   sha256sum --check checksums.txt
   ```

Publishing the Homebrew cask to `pranaysparihar/homebrew-infernosim` requires a
repository secret named `GH_PAT` with content-write access to that tap. The
release workflow fails before publication when this secret is absent so GitHub
and Homebrew cannot silently advertise different versions. After the first
cask release, remove the obsolete root-level formula from the tap; existing
v3.0.1 formula users follow the one-time migration in the README.

`GH_PAT` is required for unattended GitHub Actions publication because the
repository-scoped `GITHUB_TOKEN` cannot write to a different repository. It is
not required for a maintainer-driven release: a maintainer with write access to
both repositories may temporarily disable the tag workflow, push the annotated
tag, run GoReleaser locally with the authenticated GitHub token supplied as
both `GITHUB_TOKEN` and `GH_PAT`, verify the release and tap, and re-enable the
workflow. Do not print, commit, or persist that token.
