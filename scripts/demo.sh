#!/bin/bash
# Ensure the InfernoSIM agent is built
go build -o infernosim-agent ./cmd/agent

# Start the example service (Go app) on 8081 in the background
PORT=8081 go run examples/goapp/main.go & 
SERVICE_PID=$!
echo "Started example service (goapp) with PID $SERVICE_PID"

# Start InfernoSIM inbound sidecar on 8080 (forwarding to 8081)
./infernosim-agent --mode=inbound --listen=:8080 --forward=localhost:8081 --log=events.log & 
INBOUND_PID=$!
echo "Started InfernoSIM inbound agent (PID $INBOUND_PID)"

# Start InfernoSIM outbound proxy on 9000
./infernosim-agent --mode=proxy --listen=:9000 --log=events.log & 
PROXY_PID=$!
echo "Started InfernoSIM forward proxy agent (PID $PROXY_PID)"

# Give them a second to start up
sleep 1

# Perform a test request through the inbound proxy.
# Note: The goapp expects query param 'q', and we set HTTP_PROXY for curl to simulate service's environment if needed.
echo "Sending test request to the service via InfernoSIM..."
curl -x "" http://localhost:8080/api/test?q=Inferno

# Wait a moment for all logging to be done
sleep 2

# Kill background processes
kill $SERVICE_PID $INBOUND_PID $PROXY_PID

# Output the captured events
echo "Captured events in events.log:"
cat events.log