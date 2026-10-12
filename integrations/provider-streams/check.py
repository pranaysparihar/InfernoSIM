"""Consume engine-produced baseline/mutated streams with independent SDKs.

HTTP is loopback-only and serves recorded fixtures. No model/API is called.
"""
import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import platform
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading

for key in list(os.environ):
    if key.lower() in ("http_proxy", "https_proxy", "all_proxy"):
        os.environ.pop(key)

from openai import OpenAI
from anthropic import Anthropic
from ollama import Client as Ollama


def consume(format, base):
    if format == "responses.sse":
        with OpenAI(api_key="fixture", base_url=base + "/v1", max_retries=0, timeout=5) as client:
            with client.responses.stream(model="fixture", input="recorded fixture") as stream:
                response = stream.get_final_response()
        calls = {call.call_id: json.loads(call.arguments) for call in response.output if call.type == "function_call"}
        assert set(calls) == {"call_a", "call_b"}
        return [calls["call_a"], calls["call_b"]]
    if format == "chat.sse":
        with OpenAI(api_key="fixture", base_url=base + "/v1", max_retries=0, timeout=5) as client:
            with client.chat.completions.stream(model="fixture", messages=[{"role": "user", "content": "recorded fixture"}]) as stream:
                response = stream.get_final_completion()
        calls = {call.id: json.loads(call.function.arguments) for call in response.choices[0].message.tool_calls}
        assert set(calls) == {"call_a", "call_b"}
        return [calls["call_a"], calls["call_b"]]
    if format == "anthropic.sse":
        with Anthropic(api_key="fixture", base_url=base, max_retries=0, timeout=5) as client:
            with client.messages.stream(model="fixture", max_tokens=100, messages=[{"role": "user", "content": "recorded fixture"}]) as stream:
                response = stream.get_final_message()
        calls = {call.id: call.input for call in response.content if call.type == "tool_use"}
        assert set(calls) == {"call_a", "call_b"}
        return [calls["call_a"], calls["call_b"]]
    with Ollama(host=base, headers={"Authorization": "Bearer fixture"}, timeout=5) as client:
        chunks = list(client.chat(model="fixture", messages=[{"role": "user", "content": "recorded fixture"}], stream=True))
    assert chunks[-1].done
    return [call.function.arguments for chunk in chunks for call in (chunk.message.tool_calls or [])]


def verify(format, body, chunk_size):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_POST(self):
            self.rfile.read(int(self.headers.get("Content-Length", "0")))
            self.send_response(200)
            self.send_header("Content-Type", "application/x-ndjson" if format == "ollama.ndjson" else "text/event-stream")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            self.end_headers()
            for offset in range(0, len(body), chunk_size):
                self.wfile.write(body[offset:offset + chunk_size])
                self.wfile.flush()
            self.close_connection = True

        def log_message(self, *args):
            pass

    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
        thread.start()
        try:
            return consume(format, f"http://127.0.0.1:{server.server_port}")
        finally:
            server.shutdown()
            thread.join()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("fixtures", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    report = {"python": platform.python_version(), "platform": platform.system() + "/" + platform.machine(),
              "sdks": {name: importlib.metadata.version(name) for name in ["openai", "anthropic", "ollama"]},
              "passed": False, "cases": []}
    for format in ["responses.sse", "chat.sse", "anthropic.sse", "ollama.ndjson"]:
        for mode in ["baseline", "mutated"]:
            body = (args.fixtures / (mode + "-" + format)).read_bytes()
            expected = [{"account": "A", "amount": 10}, {"account": "B", "amount": 99 if mode == "mutated" else 20}]
            for chunk_size in [1, 17, 4096]:
                actual = verify(format, body, chunk_size)
                assert actual == expected, (format, mode, chunk_size, actual)
                report["cases"].append({"format": format, "mode": mode, "write_chunk_size": chunk_size,
                                        "sha256": hashlib.sha256(body).hexdigest(), "passed": True})
                print(format, mode, chunk_size, "PASS", flush=True)
    report["passed"] = True
    args.out.write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
