"""Concurrent application fixtures with independent SQLite and file oracles.

All state and dependency responses are synthetic. The unsafe modes contain
deliberate application bugs; checks never read InfernoSIM's effect ledger.
"""

import argparse
import base64
import concurrent.futures
import contextlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import threading
import urllib.error
import urllib.request
import uuid


LOCK = threading.Lock()
STATE = Path(os.environ.get("INFERNOSIM_STATE_DIR", "."))
BUSINESS = {"business_key": "payment-1"}


@contextlib.contextmanager
def transaction():
    connection = sqlite3.connect(STATE / "application.sqlite", timeout=10, isolation_level=None)
    try:
        connection.execute("BEGIN IMMEDIATE")
        yield connection
        connection.execute("COMMIT")
    except BaseException:
        connection.execute("ROLLBACK")
        raise
    finally:
        connection.close()


def journal(value):
    with LOCK, (STATE / "application.jsonl").open("a", encoding="utf-8") as output:
        output.write(json.dumps(value, sort_keys=True) + "\n")


def setup():
    STATE.mkdir(parents=True, exist_ok=True)
    with contextlib.closing(sqlite3.connect(STATE / "application.sqlite")) as connection:
        connection.executescript("""
            PRAGMA journal_mode=WAL;
            CREATE TABLE account(id INTEGER PRIMARY KEY, version INTEGER NOT NULL, balance INTEGER NOT NULL);
            INSERT INTO account VALUES(1, 0, 100);
            CREATE TABLE outbox(id TEXT PRIMARY KEY, business_key TEXT NOT NULL);
            CREATE TABLE idempotency(key TEXT PRIMARY KEY);
            CREATE TABLE reservations(key TEXT PRIMARY KEY);
        """)
        connection.commit()
    (STATE / "application.jsonl").write_text("", encoding="utf-8")


def outbox(safe):
    with transaction() as connection:
        if safe:
            cursor = connection.execute("INSERT OR IGNORE INTO idempotency VALUES(?)", ("payment-1",))
            if cursor.rowcount == 0:
                return
        connection.execute("INSERT INTO outbox VALUES(?, ?)", (str(uuid.uuid4()), "payment-1"))
    journal({"event": "outbox", "business_key": "payment-1"})


def reserve():
    with transaction() as connection:
        connection.execute("INSERT INTO reservations VALUES(?)", ("payment-1",))
        connection.execute("UPDATE account SET balance=50 WHERE id=1")
    journal({"event": "reserved"})


def compensate():
    with transaction() as connection:
        connection.execute("DELETE FROM reservations WHERE key=?", ("payment-1",))
        connection.execute("UPDATE account SET balance=100 WHERE id=1")
    journal({"event": "compensated"})


def request(tool, rpc_id=None):
    return {"jsonrpc": "2.0", "id": rpc_id or str(uuid.uuid4()), "method": "tools/call",
            "params": {"name": tool, "arguments": BUSINESS}}


def post(tool):
    proxy = os.environ["INFERNOSIM_PROXY_URL"]
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({"http": proxy}))
    payload = request(tool)
    req = urllib.request.Request("http://dependency.test/tools", data=json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json"})
    try:
        with opener.open(req, timeout=5) as response:
            result = json.load(response)
            if result["id"] != payload["id"]:
                raise RuntimeError(
                    "response correlation changed: this fixture generates fresh JSON-RPC IDs. "
                    "Use agent causal discover, then agent explore --causal-graph; "
                    "see docs/RETRY_REPRODUCTION.md or scripts/retry-repro.sh. "
                    "Ordinary agent run does not remap these HTTP response IDs."
                )
            return result["result"]
    except (urllib.error.URLError, ConnectionError, TimeoutError):
        return None


class MCP:
    def __init__(self):
        self.process = subprocess.Popen(json.loads(os.environ["INFERNOSIM_MCP_COMMAND_JSON"]),
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        self.pending = {}
        self.lock = threading.Lock()
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
        code = self.process.wait(timeout=5)
        self.reader.join(timeout=5)
        self.process.stdout.close()
        if code or self.reader.is_alive():
            raise RuntimeError("stdio connector did not finish cleanly")


def run(scenario, safe):
    if scenario == "cancel":
        reserve()
        client = MCP()
        client.send({"jsonrpc": "2.0", "id": str(uuid.uuid4()), "method": "initialize", "params": init_params()}).result(5)
        client.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        slow = request("slow")
        pending = client.send(slow)
        client.send(request("audit")).result(5)
        client.send({"jsonrpc": "2.0", "method": "notifications/cancelled", "params": {"requestId": slow["id"], "reason": "fixture cancellation"}})
        pending.cancel()
        if safe:
            compensate()
        client.close()
        return
    if scenario == "restart":
        outbox(safe)
        if os.environ.get("INFERNOSIM_ATTEMPT") == "0":
            (STATE / "checkpoint.json").write_text(json.dumps(BUSINESS), encoding="utf-8")
            post("refund")
            raise RuntimeError("runner must stop the first attempt before its response")
        if json.loads((STATE / "checkpoint.json").read_text()) != BUSINESS:
            raise RuntimeError("durable checkpoint missing")
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            list(pool.map(post, ["refund_status", "audit"]))
        return
    if scenario == "stale":
        def apply(tool):
            result = post(tool)
            if result is None:
                raise RuntimeError("unexpected lookup transport failure")
            version = result["version"]
            with transaction() as connection:
                if safe:
                    connection.execute("UPDATE account SET version=MAX(version,?) WHERE id=1", (version,))
                else:
                    connection.execute("UPDATE account SET version=? WHERE id=1", (version,))
            journal({"event": "version", "version": version})
            if tool == "read_new":
                post("checkpoint_new")
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            list(pool.map(apply, ["read_old", "read_new"]))
        return
    with concurrent.futures.ThreadPoolExecutor(2) as pool:
        audit = pool.submit(post, "audit")
        if scenario == "retry":
            outbox(safe)
            if post("refund") is None:
                # Retry of local work after remote commit and response loss.
                outbox(safe)
            post("refund_status")
        elif scenario == "compensate":
            reserve()
            post("reserve")
            result = post("commit")
            if safe and (result is None or not result["ok"]):
                compensate()
        else:
            raise ValueError("unknown scenario")
        audit.result(5)


def check(scenario):
    with contextlib.closing(sqlite3.connect(f"file:{STATE / 'application.sqlite'}?mode=ro", uri=True)) as connection:
        version, balance = connection.execute("SELECT version,balance FROM account WHERE id=1").fetchone()
        rows = connection.execute("SELECT business_key FROM outbox").fetchall()
        reservations = connection.execute("SELECT COUNT(*) FROM reservations").fetchone()[0]
    entries = [json.loads(line) for line in (STATE / "application.jsonl").read_text().splitlines()]
    if scenario == "stale":
        database = version == 2
        filesystem = sorted(row["version"] for row in entries) == [1, 2]
    elif scenario in ("retry", "restart"):
        database = rows == [("payment-1",)]
        filesystem = len(entries) == 1 and entries[0]["event"] == "outbox"
        if scenario == "restart":
            filesystem = filesystem and json.loads((STATE / "checkpoint.json").read_text()) == BUSINESS
    else:
        database = balance == 100 and reservations == 0
        filesystem = [row["event"] for row in entries] == ["reserved", "compensated"]
    print(json.dumps({"assertions": [{"id": "database", "passed": database}, {"id": "filesystem", "passed": filesystem}]}))


def init_params():
    return {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "concurrencylab", "version": "1"}}


def initialize(directory, scenario):
    root = Path(directory)
    root.mkdir(parents=True, exist_ok=False)
    edges = []
    result = {"ok": True}
    configs = {"stale": ["read_old", "read_new", "checkpoint_new"], "retry": ["refund", "refund_status", "audit"],
               "compensate": ["reserve", "commit", "audit"], "restart": ["refund", "refund_status", "audit"]}
    events = []
    records = []
    if scenario == "cancel":
        def record(direction, message):
            records.append({"version": 1, "sequence": len(records) + 1, "direction": direction, "message": message})
        record("client_to_server", {"jsonrpc": "2.0", "id": "init", "method": "initialize", "params": init_params()})
        record("server_to_client", {"jsonrpc": "2.0", "id": "init", "result": {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}}, "serverInfo": {"name": "fixture", "version": "1"}}})
        record("client_to_server", {"jsonrpc": "2.0", "method": "notifications/initialized"})
        record("client_to_server", request("slow", "slow"))
        record("client_to_server", request("audit", "audit"))
        record("server_to_client", {"jsonrpc": "2.0", "id": "audit", "result": result})
        record("client_to_server", {"jsonrpc": "2.0", "method": "notifications/cancelled", "params": {"requestId": "slow", "reason": "fixture cancellation"}})
        edges.append({"from": "mcp_5:release", "to": "mcp_7:admit", "reason": "application cancels slow work after audit returns"})
    else:
        for tool in configs[scenario]:
            payload = dict(result)
            if tool in ("read_old", "read_new"):
                payload = {"version": 1 if tool == "read_old" else 2}
            if tool == "commit":
                payload = {"ok": False}
            response = {"jsonrpc": "2.0", "id": tool, "result": payload}
            events.append({"id": tool, "type": "OutboundCall", "method": "POST", "url": "http://dependency.test/tools", "status": 200,
                           "bodyB64": base64.b64encode(json.dumps(request(tool, tool)).encode()).decode(), "responseCaptured": True,
                           "responseHeaders": {"Content-Type": ["application/json"]}, "responseBodyB64": base64.b64encode(json.dumps(response).encode()).decode()})
        pairs = {"stale": [("read_new", "checkpoint_new")], "retry": [("refund", "refund_status")],
                 "compensate": [("reserve", "commit")], "restart": [("refund", "refund_status"), ("refund", "audit")]}
        for before, after in pairs[scenario]:
            edges.append({"from": before + ":release", "to": after + ":admit", "reason": "application awaits this response before issuing the continuation"})
        if scenario == "stale":
            # This is an explicit regression precondition, not an inferred
            # application dependency: the old value arrives after the new
            # transaction's observable checkpoint. Network release alone does
            # not prove that the application has committed its transaction.
            edges.append({"from": "checkpoint_new:admit", "to": "read_old:release",
                          "reason": "Explore the stale-response regression after the new-value transaction checkpoint is observed."})
    config = {"agent": {"enabled": True, "adapters": {"mcp": True}, "limits": {"max_calls": 32},
                        "assertions": [{"id": "recorded-only", "type": "no_unexpected_calls"}]}}
    noise_tool = "read_new" if scenario == "stale" else "audit"
    config["agent"]["faults"] = [{"id": "noise", "select": {"tool": noise_tool}, "headers": {"X-Fixture-Noise": "yes"}}]
    if scenario == "retry":
        config["agent"]["effects"] = [{"name": "refund", "select": {"tool": "refund"}, "identity": ["$.arguments.business_key"], "deduplicate_by": ["$.arguments.business_key"]}]
        config["agent"]["faults"].append({"id": "response-lost", "select": {"tool": "refund"}, "committed_response_lost": True})
        config["agent"]["assertions"].append({"id": "remote-once", "type": "exactly_once_effect", "effect": "refund"})
    files = {"incident.json": json.dumps({"schema_version": 1}), "inbound.log": "", "outbound.log": "".join(json.dumps(e) + "\n" for e in events),
             "replay.yaml": json.dumps(config, indent=2), "declarations.json": json.dumps({"review": "fixture dependencies are explicitly declared; remaining requests are independent", "edges": edges}, indent=2)}
    if records:
        files["mcp.log"] = "".join(json.dumps(r) + "\n" for r in records)
    for name, data in files.items():
        (root / name).write_text(data, encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--scenario", choices=["stale", "retry", "cancel", "compensate", "restart"], required=True)
    parser.add_argument("--init")
    parser.add_argument("--setup", action="store_true")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--mode", choices=["safe", "unsafe"], default="safe")
    args = parser.parse_args()
    if args.init:
        initialize(args.init, args.scenario)
    elif args.setup:
        setup()
    elif args.check:
        check(args.scenario)
    else:
        run(args.scenario, args.mode == "safe")
