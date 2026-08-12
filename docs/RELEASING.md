# Release process

InfernoSIM releases are produced from annotated `v*` tags by
`.github/workflows/release.yml`.

## Maintainer checklist

1. Confirm `go test -race ./...`, nested Testcontainers module tests,
   `go vet ./...`, `go mod tidy -diff`, the fuzz smoke suite, Docker builds,
   both Compose smoke profiles, `scripts/kafka-smoke.sh`, and the 100-run
   category benchmark pass.
2. Update `RELEASE_NOTES.md` and `docs/UPGRADING.md`.
3. Run `goreleaser check` and a local snapshot with
   `goreleaser release --snapshot --clean`.
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

The InfernoSIM release workflow uses only its repository-scoped `GITHUB_TOKEN`.
The separate `pranaysparihar/homebrew-infernosim` repository checks the latest
public release on a schedule and on manual dispatch, generates a source-built
formula with `scripts/update-homebrew-formula.sh`, and pushes it with that
repository's own `GITHUB_TOKEN`. No cross-repository personal access token is
required. Building from the tagged source avoids requiring users to bypass
macOS Gatekeeper.

A maintainer-driven release also needs no stored personal access token. A
maintainer with write access may temporarily disable the tag workflow, push the
annotated tag, run GoReleaser locally with the authenticated GitHub token
supplied as `GITHUB_TOKEN`, clone the tap, and generate the formula with:

```bash
scripts/update-homebrew-formula.sh v3.4.0 /path/to/homebrew-infernosim/Formula/infernosim.rb
```

Commit and push the formula using the maintainer's existing Git credentials,
verify the release and tap, and re-enable the workflow. Do not print, commit,
or persist the authenticated token. Alternatively, manually dispatch the tap's
formula-update workflow after the GitHub Release is public.
