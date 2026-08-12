# Incident to deterministic CI test

InfernoSIM v3.4 turns a sanitized incident directory into a local dependency
simulator, an editable test harness, and a proof artifact. The workflow remains
local; no InfernoSIM account or hosted control plane is required.

## 1. Capture and sanitize

Capture HTTP, HTTPS, and gRPC traffic with a privacy policy:

```bash
export INFERNOSIM_TOKEN_KEY='replace-with-at-least-16-random-bytes'

infernosim capture \
  --forward 127.0.0.1:8080 \
  --out ./incidents/payment-timeout \
  --https-mode mitm \
  --privacy-policy ./privacy.yaml
```

Kafka-compatible events are captured from explicitly selected topics. The
broker client supports TLS, mTLS, SASL/PLAIN, and SCRAM. Pass passwords through
an environment variable; they are never accepted as a command-line value.

```bash
infernosim kafka capture \
  --brokers 127.0.0.1:9092 \
  --topics payment.authorized,payment.failed \
  --out ./incidents/payment-timeout/messages.log \
  --max-messages 20 \
  --direction publish \
  --privacy-policy ./privacy-kafka.yaml
```

Kafka capture uses the consumer API. It is not a transparent Kafka wire proxy,
so applications do not need to route broker connections through InfernoSIM.
It requires a privacy policy unless `--capture-sensitive-data` is explicitly
provided. A privacy policy must set `capture_bodies: true` to retain a
replayable, transformed payload; otherwise capture fails unless the unsafe
override is also explicit. That override can store raw keys, headers, and
payloads.

## 2. Generate explainable matcher proposals

Healing requires at least three observations of an interaction. Add more
incident directories with repeatable `--sample` arguments:

```bash
infernosim heal ./incidents/payment-timeout-1 \
  --sample ./incidents/payment-timeout-2 \
  --sample ./incidents/payment-timeout-3 \
  --out ./incidents/payment-timeout-1/replay.proposed.yaml
```

The command creates a proposed replay configuration plus JSON and HTML reports.
It never replaces `replay.yaml` by default. Review and promote it explicitly:

```bash
infernosim lint ./incidents/payment-timeout-1/replay.proposed.yaml
cp ./incidents/payment-timeout-1/replay.proposed.yaml \
   ./incidents/payment-timeout-1/replay.yaml
```

Alternatively, `--apply` performs that explicit promotion and first saves the
existing active configuration as `replay.yaml.bak`.

The healer currently proposes narrow UUID, RFC3339 timestamp, hexadecimal,
base64url, and integer timestamp/counter patterns only for allowlisted volatile
locations such as request, trace, correlation, nonce, and timestamp fields. A
UUID in `user_id`, `account_id`, or another identity/business location remains
exact. It validates a proposed pattern against a held-out observation.
Authorization, credentials, tenant, account, permission, price, currency,
status, and personal-data fields are protected from automatic relaxation. If a
matcher would make distinct captured responses ambiguous, healing fails without
writing a configuration. Re-running healing also removes stale `heal-*` rules
that current evidence no longer supports.

## 3. Validate contracts and causal workflow

```bash
infernosim contract ./incidents/payment-timeout-1 \
  --spec ./openapi.yaml

infernosim kafka validate ./incidents/payment-timeout-1 \
  --asyncapi ./asyncapi.yaml

infernosim workflow verify ./incidents/payment-timeout-1 \
  --config ./incidents/payment-timeout-1/replay.yaml
```

Workflows verify ordered HTTP, gRPC, and Kafka observations, optional required
correlation IDs, and per-step timing bounds. They emit JUnit, SARIF, and HTML
artifacts and fail CI on missing, reordered, uncorrelated, or late steps.

## 4. Generate the CI harness

For a Go application:

```bash
infernosim testgen ./incidents/payment-timeout-1 \
  --framework go-testcontainers \
  --out ./integration/infernosim

go get github.com/testcontainers/testcontainers-go@v0.44.0
go test ./integration/...
```

The generated source starts an unprivileged InfernoSIM container, keeps host
incident files owner-only, copies read-only files into the isolated container,
waits on `/healthz`, and resets state before a test.
Point the application under test's `HTTP_PROXY` and `HTTPS_PROXY` at the
returned `ProxyURL`.

Docker Compose and GitHub Actions are also generated:

```bash
infernosim testgen ./incidents/payment-timeout-1 \
  --framework docker-compose --out ./integration/infernosim

infernosim testgen ./incidents/payment-timeout-1 \
  --framework github-actions --out ./.github/workflows
```

The maintained Go adapter lives in `integrations/testcontainers-go`. Generated
harnesses are intentionally regular source code so teams can audit and edit
their test lifecycle.

## 5. Run manually and collect proof

```bash
infernosim serve ./incidents/payment-timeout-1 \
  --listen 127.0.0.1:19000 \
  --admin-listen 127.0.0.1:19001

curl http://127.0.0.1:19001/healthz
curl -X POST http://127.0.0.1:19001/__infernosim/reset
curl http://127.0.0.1:19001/__infernosim/status
curl http://127.0.0.1:19001/__infernosim/proof
```

The control service is separate from the simulated dependency port. Its proof
contains incident/config hashes, counters, divergence reasons, and a semantic
hash. It does not expose captured payloads.

## Kafka replay and deterministic failures

Validate before producing and isolate replay topics with a prefix:

```bash
infernosim kafka replay ./incidents/payment-timeout-1 \
  --brokers 127.0.0.1:9092 \
  --asyncapi ./asyncapi.yaml \
  --topic-prefix ci. \
  --drop-every 5 \
  --duplicate-every 7 \
  --poison-every 11 \
  --reorder-window 3
```

Fault selection is positional and deterministic. Replay emits JUnit, SARIF,
HTML, and `infernosim-kafka-proof.json`. A non-zero exit means connection,
contract, production, or report generation failed.

## Current boundaries

- Kafka-compatible brokers are supported; RabbitMQ, NATS, MQTT, SQS, and SNS
  are not claimed by v3.4.
- AsyncAPI 3.x JSON payload validation is supported. Avro and Schema Registry
  compatibility are not yet implemented.
- Kafka capture uses topic subscriptions, not a protocol-transparent proxy.
- The healer is deterministic rule inference, not an LLM. It intentionally
  declines values it cannot classify safely.
- Bidirectional gRPC streaming still has the limitations documented in the
  release notes.
