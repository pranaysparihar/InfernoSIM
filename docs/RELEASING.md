# Release process

InfernoSIM releases are produced from annotated `v*` tags by
`.github/workflows/release.yml`.

## Maintainer checklist

1. Confirm `go test -race ./...`, `go vet ./...`, `go mod tidy -diff`, the fuzz
   smoke suite, Docker builds, and both Compose smoke profiles pass.
2. Update `RELEASE_NOTES.md` and `docs/UPGRADING.md`.
3. Run `goreleaser check` and a local snapshot with `goreleaser release
   --snapshot --clean`.
4. Create and push an annotated version tag.
5. Verify the GitHub release contains platform archives, `checksums.txt`,
   per-archive SPDX SBOMs, and `checksums.txt.sigstore.json`.
6. Verify the checksum bundle:

   ```bash
   cosign verify-blob \
     --bundle checksums.txt.sigstore.json \
     checksums.txt
   ```

The release workflow uses GitHub OIDC for keyless Sigstore signing. Publishing
the Homebrew cask to `pranaysparihar/homebrew-infernosim` requires a repository
secret named `GH_PAT` with content-write access to that tap. Without it, GitHub's
repository-scoped token cannot update the separate tap repository.
