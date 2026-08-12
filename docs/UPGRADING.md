# Upgrading InfernoSIM

## v3.3 to v3.4

Existing incident directories and `replay.yaml` files remain valid. All v3.4
configuration is opt-in.

### Incident-to-test workflow

`infernosim serve` runs an incident as a standalone dependency simulator with
separate proxy and control ports. `infernosim testgen` can generate a Go
Testcontainers test, Docker Compose configuration, or GitHub Actions workflow.
Generated files are private by default and are never overwritten unless
`--force` is supplied.

### Guarded matcher healing

`infernosim heal` writes proposals to `replay.proposed.yaml`. It does not modify
`replay.yaml` unless `--apply` is explicit, and an applied change first creates
`replay.yaml.bak`. Authentication, tenant, identity, permission, money, status,
and personal-data fields remain protected from automatic relaxation.

### Kafka and cross-protocol workflows

Kafka capture requires an explicit topic list and either a privacy policy or
the explicit unsafe raw-capture override. Policy-based replayable capture must
set `capture_bodies: true`. Broker passwords are read from environment
variables rather than command-line flags.

AsyncAPI validation currently targets AsyncAPI 3.x JSON messages and local
references. Cross-protocol workflow verification can order HTTP, gRPC, and
Kafka observations with optional correlation and timing constraints.

### CI reports

Workflow, AsyncAPI, and Kafka commands can emit JUnit, SARIF, and HTML reports.
Treat those outputs as build artifacts; they are intentionally not attached to
the GitHub release.

## v3.2 to v3.3

Existing incident directories and `replay.yaml` files remain valid. The v3.3
features are opt-in.

### Dynamic responses

Static `body` and `body_base64` responses behave exactly as before. Use
`body_template` only when a scenario response must derive data from the runtime
request. A response may configure only one of `body`, `body_base64`,
`body_template`, `protobuf_json`, or `protobuf_stream`.

Templates use a sandboxed, deterministic function set. They cannot read files,
open sockets, execute commands, or access environment variables. Set
`templates.seed` to preserve generated IDs and timestamps across machines.

### Descriptor-aware gRPC

Captured wire-level gRPC replay still works without schema configuration. Add
`matching.grpc.proto_files` or `matching.grpc.descriptor_sets` only when using
Protobuf field predicates, semantic Protobuf comparison, or synthesized
responses.

Relative schema paths are resolved from the directory containing
`replay.yaml`. Descriptor sets must be binary `google.protobuf.FileDescriptorSet`
files containing their imports.

### CLI additions

- `infernosim generate` creates a reviewable `replay.yaml` from OpenAPI or
  Protobuf schemas.
- `infernosim lint` performs strict parsing plus scenario reachability and
  shadowing checks.
- `infernosim match explain` reports why each captured dependency call matched
  or failed.

Generation refuses to overwrite a file unless `--force` is supplied.

## v3.1 to v3.2

HTTPS dependency stubbing remains opt-in. Applications must trust the
InfernoSIM replay CA, and every MITM hostname must be explicitly allowlisted.
Existing HTTP-only replay configurations continue to work unchanged.

Incident bundle v2 is an encrypted wrapper around the existing directory
layout. Opening a v2 bundle restores an ordinary incident directory; no
permanent migration is required.
