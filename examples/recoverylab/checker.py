"""Independent read-only destination oracle; never reads simulator counters."""
import argparse
import json
from pathlib import Path
from provider import STATE, evidence


def check(scenario, variant):
    actual = evidence()
    executions = actual["executions"]
    history = actual["history"]
    original = [r for r in executions if r["role"] == "original"]
    history_original = [r for r in history if r["role"] == "original"]
    duplicates = [r for r in history if r["role"] == "retry"]
    unfinished = [r for r in executions if r["state"] != "complete"]
    decision = json.loads((STATE / "decision.json").read_text())
    audit = actual["audit"]
    drained = bool(audit) and any(r["kind"] == "drained" for r in audit)
    destination = len(original) == len(history_original) == 1 and not duplicates and not unfinished and drained
    authorization = not any(r["admitted_after_cancel"] for r in executions)
    if scenario == "commit-cancel":
        knowledge = any(r["kind"] == "cancel" for r in audit) and decision["outcome"] == "cancelled_after_commit"
    elif scenario == "pending":
        knowledge = decision["outcome"] == "complete" and decision.get("handle") == original[0]["handle"]
        pending_evidence = json.loads((STATE / "before-drain.json").read_text())
        expected_before = "unknown" if variant in ("failed-read", "stale-read") else "pending"
        knowledge = knowledge and pending_evidence["outcome"] == expected_before and pending_evidence.get("handle") == original[0]["handle"]
        knowledge = knowledge and any(r["kind"] == "status_read" for r in audit)
    else:
        resolvable = variant in ("recovered-identity", "active-dedupe")
        knowledge = decision["outcome"] == ("complete" if resolvable else "unknown")
        if resolvable:
            knowledge = knowledge and decision.get("handle") == original[0]["handle"]
        else:
            knowledge = knowledge and not decision.get("handle")
        observation = decision["observation"]
        knowledge = knowledge and any(r["kind"] == "content_lookup" for r in audit)
        if variant == "identical-other":
            knowledge = knowledge and observation.get("items") == [{"business": "payment-1", "role": "unrelated"}]
        if variant in ("delayed-visibility", "deleted"):
            knowledge = knowledge and observation.get("read_ok") and observation.get("items") == []
        if variant == "failed-read":
            knowledge = knowledge and observation.get("read_ok") is False
        if variant == "wrong-receipt":
            knowledge = knowledge and observation["receipt"]["handle"] != original[0]["handle"]
    result = {"assertions": [
        {"id": "destination", "passed": destination},
        {"id": "authorization", "passed": authorization},
        {"id": "knowledge", "passed": knowledge},
    ]}
    print(json.dumps(result))


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--scenario", required=True)
    p.add_argument("--variant", required=True)
    a = p.parse_args()
    check(a.scenario, a.variant)
