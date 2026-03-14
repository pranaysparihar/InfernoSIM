#!/bin/bash
set -e

# Constants for test strictness
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$DIR")"
BUILD_BIN="build/infernosim-agent"
TEST_GOAPP="build/test-goapp"
TEST_GRPCAPP="build/test-grpcapp"
TEST_HTTPSAPP="build/test-httpsapp"

echo "====================================="
echo " InfernoSIM E2E Validation Suite v1 "
echo "====================================="
echo "====================================="

cd "$PROJECT_ROOT"

# Ensure cleanup of testing artifacts
trap 'rm -rf build events_*.log mutated_events.log proxy_https.log client_https.log' EXIT

echo "[1/7] Running Unit Tests..."
go test ./...

echo "[2/7] Building Agent and Test Binaries..."
mkdir -p build
go build -o $BUILD_BIN ./cmd/agent
go build -o $TEST_GOAPP examples/goapp/main.go
go build -o $TEST_GRPCAPP examples/grpcapp/main.go
go build -o $TEST_HTTPSAPP examples/httpsapp/main.go

# Cleanup (in case it existed before trap bounds)
rm -f events_*.log

echo "[3/7] Generating CA for HTTPS Tests (if missing)..."
./$BUILD_BIN --mode=proxy --https-mode=mitm --listen=:9001 &
INIT_PID=$!
sleep 2
kill $INIT_PID 2>/dev/null || true
wait $INIT_PID 2>/dev/null || true

# Test 1: Bounded HTTP capture
echo "[4/7] Testing Bounded HTTP Capture & HTTP Pipeline (Proxy)..."
HTTP_PROXY=http://localhost:9000 PORT=8081 ./$TEST_GOAPP >/dev/null 2>&1 &
APP_PID=$!
./$BUILD_BIN --mode=proxy --listen=:9000 --log=events_http.log >/dev/null 2>&1 &
PROXY_PID=$!

sleep 2
curl -s -x "http://localhost:9000" -d "full_body_validation_payload" "http://localhost:8081/api/test?q=Verification" >/dev/null

sleep 2
kill $PROXY_PID $APP_PID 2>/dev/null || true
wait

# Assert Output
if ! grep -q '"bodySha256"' events_http.log; then
    echo "FAIL: Missing payload tracking in events_http.log"
    exit 1
fi
echo "✓ Bounded HTTP Capture Verified"

# Test 2: gRPC Unary
echo "[5/7] Testing gRPC Unary Tracking..."
./$TEST_GRPCAPP --mode=server --addr=:50051 >/dev/null 2>&1 &
GRPC_S_PID=$!
./$BUILD_BIN --mode=proxy --listen=:9000 --log=events_grpc.log >/dev/null 2>&1 &
PROXY2_PID=$!

sleep 2
HTTP_PROXY=http://localhost:9000 ./$TEST_GRPCAPP --mode=client --target=localhost:50051 --msg=HelloGRPC >/dev/null 2>&1 || true

sleep 2
kill $GRPC_S_PID $PROXY2_PID 2>/dev/null || true
wait

if ! grep -q '"grpcServiceMethod"' events_grpc.log; then
    echo "FAIL: Missing gRPC telemetry tracking in events_grpc.log"
    exit 1
fi
echo "✓ gRPC Visibility Verified"

# Test 3: MitM Deep Target Inspection
echo "[6/7] Testing Outbound MITM Decryption (HTTPS CONNECT)..."
./$TEST_HTTPSAPP --mode=server --addr=:8443 >/dev/null 2>&1 &
HTTPS_S_PID=$!

./$BUILD_BIN --mode=proxy --listen=:9000 --https-mode=mitm --log=events_https.log >proxy_https.log 2>&1 &
PROXY3_PID=$!

sleep 2
./$TEST_HTTPSAPP --mode=client --addr=:8443 --proxy=http://localhost:9000 >client_https.log 2>&1 || true

sleep 2
kill $HTTPS_S_PID $PROXY3_PID 2>/dev/null || true
wait

if ! grep -q "\/api\/secure" events_https.log; then
    echo "FAIL: HTTPS Deep capture did not decrypt the payload (expected '/api/secure' in logs)"
    exit 1
fi
echo "✓ MITM HTTP Pipeline Decryption Verified"

# Test 4: Search Engine Constraint Failure Under Severe Injection
echo "[7/7] Testing Fault Injection and Search Constraint Mapping..."
./$TEST_GOAPP >/dev/null 2>&1 &
APP2_PID=$!

# Execute Search bounded by SLO injection
# Start proxy with explicit high failure fault injection
./$BUILD_BIN --mode=proxy --listen=:9000 --inject="status=503,rate=100%" --log=events_faults.log >/dev/null 2>&1 &
PROXY4_PID=$!
sleep 2

# Force generating logs requiring strict adherence
curl -s -x "http://localhost:9000" "http://localhost:8081/api/test?q=FailTest" >/dev/null
sleep 2

kill $PROXY4_PID 2>/dev/null || true
wait $PROXY4_PID 2>/dev/null || true

# Assert Replay throws strict fault
set +e
OUT=$(./$BUILD_BIN strict-replay --log=events_faults.log --target=http://localhost:8081 2>&1)
set -e

if echo "$OUT" | grep -q "FAIL_SLO_MISSED"; then
    echo "✓ Strict Error Mapping and Outcome Constraints Enforced"
else
    echo "FAIL: Proxy did not correctly synthesize the strict SLO fault mapping on error."
    echo "$OUT"
    exit 1
fi

# Test 5: Replay Engine Hash Mismatch Validation
echo "[8/8] Testing Payload Determinism vs Mutations (FAIL_NON_DETERMINISTIC)..."
sed 's/"bodySha256":"[a-f0-9]*"/"bodySha256":"d3adbeef11e7838f6edc27250b32cfa4c3629534a3334405da6a66951941765"/' events_http.log > mutated_events.log

set +e
OUT=$(./$BUILD_BIN strict-replay --log=mutated_events.log --target=http://localhost:8081 2>&1)
set -e

if echo "$OUT" | grep -q "FAIL_NON_DETERMINISTIC"; then
    echo "✓ Hash Fingerprint Divergences Rejection Verified"
else
    echo "FAIL: Proxy did not correctly synthesize the determinism fault parsing."
    echo "$OUT"
    exit 1
fi

kill $APP2_PID 2>/dev/null || true
wait $APP2_PID 2>/dev/null || true

# Test 6: Search Envelope Boundaries Extrapolating Fault Loads
echo "[9/9] Testing Envelope Fanout Boundary Mapping..."
./$TEST_GOAPP >/dev/null 2>&1 &
APP3_PID=$!
sleep 2

set +e
SEARCH_OUT=$(./$BUILD_BIN search --log=events_http.log --target=http://localhost:8081 2>&1)
set -e

kill $APP3_PID 2>/dev/null || true

if echo "$SEARCH_OUT" | grep -q "Envelope stable up to fanout"; then
    echo "✓ Native Fanout Load Envelope Identified Successfully"
else
    echo "FAIL: Proxy did not determine execution fanouts gracefully."
    echo "$SEARCH_OUT"
    exit 1
fi

echo "====================================="
echo " ✓ ALL VERIFICATION E2E PATHS CLEAR "
echo "====================================="
