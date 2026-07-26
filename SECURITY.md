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
