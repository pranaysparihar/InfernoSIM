# Upgrading InfernoSIM

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
