"""Bounded community regressions against a separate synthetic SQLite provider.

The harness drains provider work after the client exits, then reconciles bound
receipts. The checker is a separate read-only process with its own fingerprint.
"""
import argparse
import base64
import concurrent.futures
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import urllib.error
import urllib.request
import uuid

import provider

ROOT = Path(__file__).resolve().parent
STATE = provider.STATE


def call_provider(action, **values):
    output = subprocess.check_output([sys.executable, str(ROOT / "provider.py"), action,
                                      "--values", json.dumps(values)], timeout=5)
    return json.loads(output)


def save(name, value):
    temporary = STATE / (name + ".tmp")
    with temporary.open("w", encoding="utf-8") as stream:
        json.dump(value, stream, sort_keys=True)
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(STATE / name)


def request(name, rpc_id=None):
    return {"jsonrpc": "2.0", "id": rpc_id or str(uuid.uuid4()), "method": "tools/call",
            "params": {"name": name, "arguments": {"business_key": "payment-1"}}}


def post(name):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({"http": os.environ["INFERNOSIM_PROXY_URL"]}))
    payload = request(name)
    req = urllib.request.Request("http://recovery.test/tools", data=json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json", "X-InfernoSIM-Operation-ID": name})
    try:
        with opener.open(req, timeout=8) as response:
            result = json.load(response)
            if result["id"] != payload["id"]:
                raise RuntimeError("use the causal runner: fresh runtime response ID was not remapped")
            return result["result"]
    except (urllib.error.URLError, ConnectionError, TimeoutError):
        return None


class MCP:
    def __init__(self):
        self.process = subprocess.Popen(json.loads(os.environ["INFERNOSIM_MCP_COMMAND_JSON"]),
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        self.lock = threading.Lock()
        self.pending = {}
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()

    def read(self):
        for line in self.process.stdout:
            message = json.loads(line)
            with self.lock:
                future = self.pending.pop(message.get("id"), None)
            if future is not None and not future.cancelled():
                future.set_result(message)

    def send(self, message):
        future = concurrent.futures.Future()
        with self.lock:
            if "id" in message:
                self.pending[message["id"]] = future
            self.process.stdin.write(json.dumps(message) + "\n")
            self.process.stdin.flush()
        return future

    def close(self):
        self.process.stdin.close()
        code = self.process.wait(timeout=8)
        self.reader.join(timeout=5)
        self.process.stdout.close()
        if code or self.reader.is_alive():
            raise RuntimeError("MCP transport did not close cleanly")


def init_params():
    return {"protocolVersion": "2025-11-25", "capabilities": {},
            "clientInfo": {"name": "recoverylab", "version": "1"}}


def committed_cancel(safe):
    client = MCP()
    client.send({"jsonrpc": "2.0", "id": str(uuid.uuid4()), "method": "initialize", "params": init_params()}).result(8)
    client.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    original = call_provider("submit", role="original", protected=True, pending=False)
    # The independent provider process has committed before this observable
    # checkpoint. The simulator controls delivery/loss of its caller response.
    lost = post("original_response") is None
    save("response-loss.json", {"lost": lost, "original_handle": original["handle"]})
    slow = request("slow")
    pending = client.send(slow)

    def retry():
        post("retry_admit")
        if lost:
            if not safe or not call_provider("cancelled"):
                call_provider("submit", role="retry", protected=safe, pending=False)
        post("retry_finished")

    def cancel():
        client.send(request("audit")).result(8)
        client.send({"jsonrpc": "2.0", "method": "notifications/cancelled",
                     "params": {"requestId": slow["id"], "reason": "fixture cancellation"}})
        pending.cancel()
        call_provider("cancel")
        post("cancel_observed")

    with concurrent.futures.ThreadPoolExecutor(2) as pool:
        futures = [pool.submit(retry), pool.submit(cancel)]
        for future in futures:
            future.result(15)
    client.close()
    save("decision.json", {"outcome": "cancelled_after_commit", "handle": original["handle"]})


def initial_restart(scenario, variant):
    pending = scenario == "pending" or variant == "active-dedupe"
    original = call_provider("submit", role="original", protected=True, pending=pending)
    checkpoint = {"scope": provider.SCOPE, "request_hash": provider.REQUEST_HASH,
                  "business": "payment-1", "idempotency_key": "payment-1"}
    if scenario == "pending":
        checkpoint["handle"] = original["handle"]
    else:
        if variant in ("identical-other", "wrong-receipt"):
            other = call_provider("submit", role="unrelated", pending=False)
            checkpoint["untrusted_receipt"] = other["handle"]
        if variant == "deleted":
            call_provider("delete", handle=original["handle"])
        if variant == "expired-key":
            call_provider("expire")
        if variant == "revoked":
            call_provider("cancel")
    save("checkpoint.json", checkpoint)
    post("submitted")
    raise RuntimeError("runner must crash the first attempt at the submitted checkpoint")


def restarted_client(scenario, variant, safe):
    checkpoint = json.loads((STATE / "checkpoint.json").read_text())
    if checkpoint["scope"] != provider.SCOPE or checkpoint["request_hash"] != provider.REQUEST_HASH:
        raise RuntimeError("checkpoint scope changed")
    post("status")
    if scenario == "pending":
        observation = call_provider("status", handle=checkpoint["handle"], mode=variant)
        if variant == "expired-key":
            call_provider("expire")
        post("retry_considered")
        if not safe:
            call_provider("submit", role="retry", protected=False, pending=True)
        elif variant == "same-key":
            retried = call_provider("submit", role="retry", protected=True, pending=True)
            if retried["handle"] != checkpoint["handle"] or retried["state"] != "pending":
                raise RuntimeError("active deduplication contract broken")
        outcome = "pending" if observation.get("read_ok") and observation.get("state") == "pending" else "unknown"
        decision = {"outcome": outcome, "handle": checkpoint["handle"], "observation": observation,
                    "retry_permission": "active_dedupe" if safe and variant == "same-key" else "none"}
    else:
        observation = call_provider("lookup", mode=variant, other_handle=checkpoint.get("untrusted_receipt"))
        post("retry_considered")
        handle = None
        if safe and variant == "recovered-identity":
            recovered = call_provider("recover_identity")
            handle = recovered["handle"] if recovered else None
        elif safe and variant == "active-dedupe":
            recovered = call_provider("submit", role="retry", protected=True, pending=True)
            handle = recovered["handle"]
        elif not safe:
            retried = call_provider("submit", role="retry", protected=False, pending=True)
            handle = retried["handle"]
        decision = {"outcome": "pending" if handle else "unknown", "observation": observation,
                    "retry_permission": "active_dedupe" if safe and variant == "active-dedupe" else "none"}
        if handle:
            decision["handle"] = handle
    save("before-drain.json", decision)
    save("decision.json", decision)
    post("client_done")


def harness(args):
    if args.scenario == "commit-cancel":
        committed_cancel(args.mode == "safe")
        call_provider("drain")
    else:
        subprocess.run([sys.executable, str(Path(__file__).resolve()), "--scenario", args.scenario,
                        "--variant", args.variant, "--mode", args.mode, "--client"], check=True, timeout=20)
        # This is the observation harness, after the client actually exited.
        # Deferred work still commits even if the client claimed to be finished.
        call_provider("drain")
        post("drained")
        decision = json.loads((STATE / "decision.json").read_text())
        if decision.get("handle"):
            receipt = call_provider("receipt", handle=decision["handle"])
            if receipt and receipt["state"] == "complete":
                decision["outcome"] = "complete"
        save("decision.json", decision)
        post("receipt_read")
    print(json.dumps({"scenario": args.scenario, "variant": args.variant,
                      "destination": provider.evidence(), "decision": json.loads((STATE / "decision.json").read_text())}, sort_keys=True))


def initialize(directory, scenario, variant):
    root = Path(directory)
    root.mkdir(parents=True, exist_ok=False)
    names = (["original_response", "retry_admit", "retry_finished", "cancel_observed"] if scenario == "commit-cancel"
             else ["submitted", "status", "retry_considered", "client_done", "drained", "receipt_read"])
    events = []
    for name in names:
        events.append({"id": name, "type": "OutboundCall", "method": "POST", "url": "http://recovery.test/tools", "status": 200,
                       "headers": {"Content-Type": ["application/json"], "X-InfernoSIM-Operation-ID": [name]},
                       "bodyB64": base64.b64encode(json.dumps(request(name, name)).encode()).decode(), "responseCaptured": True,
                       "responseHeaders": {"Content-Type": ["application/json"]},
                       "responseBodyB64": base64.b64encode(json.dumps({"jsonrpc": "2.0", "id": name, "result": {"ok": True}}).encode()).decode()})
    edges = []
    records = []
    config = {"agent": {"enabled": True, "adapters": {"mcp": True}, "limits": {"max_calls": 32},
                        "assertions": [{"id": "recorded-only", "type": "no_unexpected_calls"}]}}
    if scenario == "commit-cancel":
        def record(direction, message):
            records.append({"version": 1, "sequence": len(records) + 1, "direction": direction, "message": message})
        record("client_to_server", {"jsonrpc": "2.0", "id": "init", "method": "initialize", "params": init_params()})
        record("server_to_client", {"jsonrpc": "2.0", "id": "init", "result": {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}}, "serverInfo": {"name": "fixture", "version": "1"}}})
        record("client_to_server", {"jsonrpc": "2.0", "method": "notifications/initialized"})
        record("client_to_server", request("slow", "slow"))
        record("client_to_server", request("audit", "audit"))
        record("server_to_client", {"jsonrpc": "2.0", "id": "audit", "result": {"ok": True}})
        record("client_to_server", {"jsonrpc": "2.0", "method": "notifications/cancelled", "params": {"requestId": "slow", "reason": "fixture cancellation"}})
        pairs = [("mcp_3:release", "original_response:admit"), ("original_response:release", "mcp_4:admit"),
                 ("original_response:release", "retry_admit:admit"), ("mcp_4:admit", "mcp_5:admit"),
                 ("mcp_5:release", "mcp_7:admit"), ("mcp_7:release", "cancel_observed:admit"),
                 ("retry_admit:release", "retry_finished:admit")]
        config["agent"]["effects"] = [{"name": "original", "select": {"tool": "original_response"}, "identity": ["$.arguments.business_key"]}]
        config["agent"]["faults"] = [{"id": "response-lost", "select": {"kind": "mcp_tool_result", "tool": "original_response"}, "committed_response_lost": True},
                                      {"id": "noise", "select": {"tool": "audit"}, "headers": {"X-Noise": "yes"}}]
    else:
        pairs = [(before + ":release", after + ":admit") for before, after in zip(names, names[1:])]
    for before, after in pairs:
        edges.append({"from": before, "to": after, "reason": "explicit fixture checkpoint or awaited continuation; provider commit is separate from response delivery"})
    files = {"incident.json": "{}", "inbound.log": "", "outbound.log": "".join(json.dumps(e) + "\n" for e in events),
             "replay.yaml": json.dumps(config, indent=2), "declarations.json": json.dumps({"review": "bounded synthetic provider; all application dependencies declared; retry/cancel branches may interleave", "edges": edges}, indent=2)}
    if records:
        files["mcp.log"] = "".join(json.dumps(r) + "\n" for r in records)
    for name, value in files.items():
        (root / name).write_text(value, encoding="utf-8")


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--scenario", required=True, choices=["commit-cancel", "pending", "lost-handle"])
    p.add_argument("--variant", default="default")
    p.add_argument("--mode", default="safe", choices=["safe", "unsafe"])
    p.add_argument("--init")
    p.add_argument("--setup", action="store_true")
    p.add_argument("--client", action="store_true")
    args = p.parse_args()
    if args.init:
        initialize(args.init, args.scenario, args.variant)
    elif args.setup:
        provider.setup()
    elif args.client:
        if os.environ.get("INFERNOSIM_ATTEMPT") == "0":
            initial_restart(args.scenario, args.variant)
        else:
            restarted_client(args.scenario, args.variant, args.mode == "safe")
    else:
        harness(args)
