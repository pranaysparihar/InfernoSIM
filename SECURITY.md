# Security Policy

## Supported Versions

Security fixes are applied to the latest mainline version.

## Reporting a Vulnerability

Do not open public issues for security vulnerabilities.

Please report privately by emailing:

- pranaysparihar@gmail.com

Include:

- Affected version/commit
- Impact assessment
- Reproduction steps or proof-of-concept
- Any suggested remediation

## Response Expectations

- Initial acknowledgement: within 5 business days
- Triage and severity assessment: as quickly as possible
- Fix timeline: based on impact and exploitability

## Disclosure

After a fix is available, we will coordinate responsible disclosure and publish release notes.

## Sensitive incident data

- Prefer the built-in secure capture defaults or an explicit privacy policy.
- Store tokenization keys outside incident directories and encrypted bundles.
- Do not place bundle passphrases directly on a command line. InfernoSIM reads
  them from `INFERNOSIM_BUNDLE_PASSPHRASE` or another explicitly selected
  environment variable.
- Treat an opened bundle as sensitive plaintext and remove it according to your
  organization's retention policy.
- Trust the InfernoSIM CA only in isolated capture/replay environments. HTTPS
  capture and stubbing issue leaf certificates only for explicitly allowlisted
  hosts.

Encrypted bundle v2 protects confidentiality and integrity at rest. It does not
replace endpoint security while the incident is being captured, replayed, or
opened.

## Simulation configuration

- Response templates use a fixed function allowlist. They cannot invoke
  commands, access the filesystem, read environment variables, or open network
  connections.
- Template source and output sizes are bounded. Treat generated output as
  untrusted data when it is consumed by an application under test.
- Protobuf source files and descriptor sets are parsed locally and may consume
  CPU or memory. Only load schemas from trusted repositories, and retain the
  built-in message and stream bounds.
- `infernosim generate` refuses to replace existing files unless `--force` is
  supplied.

## Release verification

Tagged releases include SHA-256 checksums, SPDX SBOMs, and a keyless Sigstore
bundle for the checksum manifest. Verification instructions are maintained in
[`docs/RELEASING.md`](docs/RELEASING.md).
