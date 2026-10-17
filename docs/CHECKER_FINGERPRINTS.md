# Pin the checker used by a regression

v4.2.1 records a versioned fingerprint of the supplied checker executable,
direct interpreter script, arguments, required IDs, timeout and setup presence.
This catches an edited script even when its path and assertion IDs stay the same.
The fingerprint is checked before replay, before/after the check process and
throughout reduction. Comparisons require the same pinned checker.

Direct inputs alone cannot establish the dependencies of an arbitrary program.
Use `--checker-manifest ./checker.json` to declare helpers, configuration,
lockfiles and runtime/package versions:

```json
{
  "version": 1,
  "scope": "declared-inputs",
  "files": {
    "helper": "checks/helper.py",
    "config": "checks/limits.json",
    "dependencies": "requirements.lock"
  },
  "versions": {"python": "3.13.7", "container": "sha256:YOUR_PINNED_DIGEST"},
  "public_environment": ["TEST_CHECK_MODE"],
  "secret_revisions": {"test-service-key": "opaque-config-revision-7"}
}
```

Paths are relative to the manifest. Logical names and content hashes make an
identical checker relocatable; local absolute paths are not retained. Put every
import/helper/plugin/config that affects the verdict in the manifest. The
runner hashes the executable actually resolved and direct `.py`, `.sh`, `.js`
or `.rb` scripts. Python script operands also support `-W`/`-X` option values
and extensionless scripts. Python `-m` requires a manifest naming the module
source. Inline `-c` code is hashed in the argument contract; its referenced
files, custom launchers and dynamic imports need explicit declarations. Package and runtime versions are declared provenance, not a claim
that the runner independently queried or verified a package manager. Pin the
actual installed dependency files or immutable container in your CI environment.

Reports label scope as `pinned/direct-inputs-only` or `pinned/declared-inputs`.
Neither claims a universally complete import closure. Missing declared files or
environment variables fail explicitly. Inputs are bounded to 128 declared
files (each at most 256 MiB), and a 1 MiB manifest.

Do not list mutable databases or destination files being checked. Their contents
must change the verdict without changing the checker identity. Application and
setup commands remain caller-supplied; pin setup infrastructure separately.

Only deliberately public config belongs in `public_environment` or config
files. Never declare secrets: even a public hash can disclose a guessable secret.
For secret-dependent settings, declare a non-secret opaque configuration
revision and enforce that revision's mapping in your own CI secret store. The
runner neither reads nor proves secret values from that revision. Unrelated
environment variables and credentials are not collected.

## A changed checker is a new case

A normal `agent reproduce` or `agent minimize` rejects a different fingerprint.
To deliberately change the check, supply its new explicit command/manifest and
use `agent reproduce ORIGINAL --new-case-out NEW_CASE ...`.
No application command is needed for this inert fork.
This creates a separate artifact and case identity linked by `parent_case`,
without running the application or treating the original failure as verified
by the new checker. Run the new case separately. The original is unchanged.

New exports use reproduction format v3. Legacy v1/v2 artifacts without fingerprints remain readable but are explicitly
`legacy-unpinned` in terminal, JSON and exported reports. Their results cannot
establish an unchanged-checker comparison or reduction. They can be forked explicitly into
a new pinned case; the old artifact is never retroactively called pinned.

These checks detect ordinary input changes, not malicious code that swaps a
file during execution and restores it, changes a remote config service, or
lies about undeclared dependencies. Use an immutable, isolated test environment
when those threats matter. Fingerprinting never executes artifact-supplied
commands and does not make checksums an authenticity signature.
