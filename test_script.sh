#!/bin/bash
kill -9 $(lsof -t -i:8081) 2>/dev/null || true
kill -9 $(lsof -t -i:8080) 2>/dev/null || true
kill -9 $(lsof -t -i:9000) 2>/dev/null || true

rm -f inbound.log outbound.log

go build -o infernosim-agent ./cmd/agent

# Start service WITH outbound proxy configured
HTTP_PROXY=http://localhost:9000 PORT=8081 go run examples/goapp/main.go & 
SERVICE_PID=$!
echo "Started example service PID $SERVICE_PID"

# Start inbound proxy (sidecar)
./infernosim-agent --mode=inbound --listen=:8080 --forward=localhost:8081 --log=inbound.log & 
INBOUND_PID=$!
echo "Started inbound proxy PID $INBOUND_PID"

# Start outbound proxy
./infernosim-agent --mode=proxy --listen=:9000 --log=outbound.log & 
PROXY_PID=$!
echo "Started outbound proxy PID $PROXY_PID"

sleep 3

echo -e "\n--- Sending curl request ---"
curl -s -x "" "http://localhost:8080/api/test?q=Inferno"
echo -e "\n--- Done sending request ---"

sleep 3

kill $SERVICE_PID $INBOUND_PID $PROXY_PID || true
wait

echo -e "\n--- Contents of inbound.log ---"
cat inbound.log

echo -e "\n--- Contents of outbound.log ---"
cat outbound.log

