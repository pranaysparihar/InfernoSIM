Step 0 — clean slate (mandatory)
rm -f inbound.log outbound.log
Step 1 — start outbound capture
./infernosim --mode=proxy --listen=:9000 --log=outbound.log
Step 2 — start Node app (locked down)
NODE_OPTIONS='-r global-agent/bootstrap' \
GLOBAL_AGENT_HTTP_PROXY=http://localhost:9000 \
GLOBAL_AGENT_NO_KEEPALIVE=true \
PORT=8082 \
node examples/nodeapp/app.js
Step 3 — start inbound capture
./infernosim --mode=inbound \
  --listen=:18080 \
  --forward=localhost:8082 \
  --log=inbound.log
  Step 4 — generate the incident (ONLY /api/demo)
  curl http://localhost:18080/api/demo
  
Step 5 — STOP inbound + outbound capture

(important — freeze the incident)
Step 6 — replay
./infernosim replay \
  --incident . \
  --inject "dep=worldtimeapi.org latency=+200ms"