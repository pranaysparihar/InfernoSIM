"""Synthetic provider with a real, separate SQLite destination and history.

No production API guarantees are implied. Commands are explicit fixture argv.
"""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import uuid

STATE = Path(os.environ.get("INFERNOSIM_STATE_DIR", "."))
SCOPE = "synthetic/account-1/test"
REQUEST_HASH = hashlib.sha256(b"payment-1:refund:100").hexdigest()


@contextlib.contextmanager
def database():
    connection = sqlite3.connect(STATE / "destination.sqlite", timeout=10)
    connection.row_factory = sqlite3.Row
    try:
        connection.execute("BEGIN IMMEDIATE")
        yield connection
        connection.commit()
    except BaseException:
        connection.rollback()
        raise
    finally:
        connection.close()


def audit(db, kind, handle="", detail=""):
    db.execute("INSERT INTO audit(kind,handle,detail) VALUES(?,?,?)", (kind, handle, detail))


def setup():
    STATE.mkdir(parents=True, exist_ok=True)
    with database() as db:
        db.executescript("""
          PRAGMA journal_mode=WAL;
          CREATE TABLE executions(handle TEXT PRIMARY KEY, role TEXT, business TEXT,
            request_hash TEXT, scope TEXT, idem_key TEXT, state TEXT, admitted_after_cancel INTEGER);
          CREATE TABLE history(seq INTEGER PRIMARY KEY AUTOINCREMENT, handle TEXT UNIQUE, role TEXT);
          CREATE TABLE objects(handle TEXT PRIMARY KEY, business TEXT);
          CREATE TABLE audit(seq INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT, handle TEXT, detail TEXT);
          CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT);
          INSERT INTO settings VALUES('dedupe_active','1'),('cancelled','0');
        """)


def commit(db, handle):
    row = db.execute("SELECT * FROM executions WHERE handle=?", (handle,)).fetchone()
    if row["state"] == "complete":
        return
    db.execute("INSERT INTO history(handle,role) VALUES(?,?)", (handle, row["role"]))
    db.execute("INSERT INTO objects VALUES(?,?)", (handle, row["business"]))
    db.execute("UPDATE executions SET state='complete' WHERE handle=?", (handle,))
    audit(db, "commit", handle, row["role"])


def submit(role, protected, pending):
    with database() as db:
        active = db.execute("SELECT value FROM settings WHERE key='dedupe_active'").fetchone()[0] == "1"
        if protected and active:
            prior = db.execute("SELECT * FROM executions WHERE idem_key='payment-1' AND scope=?", (SCOPE,)).fetchone()
            if prior:
                audit(db, "protected_retry", prior["handle"], prior["state"])
                return dict(prior)
        cancelled = db.execute("SELECT value FROM settings WHERE key='cancelled'").fetchone()[0] == "1"
        handle = str(uuid.uuid4())
        key = "payment-1" if role != "unrelated" else "separate-identical-request"
        db.execute("INSERT INTO executions VALUES(?,?,?,?,?,?,?,?)",
                   (handle, role, "payment-1", REQUEST_HASH, SCOPE, key, "pending", int(cancelled)))
        audit(db, "admit", handle, role)
        if not pending:
            commit(db, handle)
        return dict(db.execute("SELECT * FROM executions WHERE handle=?", (handle,)).fetchone())


def operation(action, **values):
    if action == "submit":
        return submit(values["role"], values.get("protected", False), values.get("pending", False))
    with database() as db:
        if action == "cancel":
            db.execute("UPDATE settings SET value='1' WHERE key='cancelled'")
            audit(db, "cancel", detail="pre-admitted work may finish; no new mutations authorized")
        elif action == "expire":
            db.execute("UPDATE settings SET value='0' WHERE key='dedupe_active'")
            audit(db, "key_expired", detail="expiry does not fence already admitted work")
        elif action == "delete":
            db.execute("DELETE FROM objects WHERE handle=?", (values["handle"],))
            audit(db, "manual_delete", values["handle"])
        elif action == "drain":
            for row in db.execute("SELECT handle FROM executions WHERE state='pending' ORDER BY rowid").fetchall():
                commit(db, row["handle"])
            audit(db, "drained", detail="all admitted executions settled")
        elif action == "receipt":
            row = db.execute("SELECT * FROM executions WHERE handle=? AND scope=? AND request_hash=?",
                             (values["handle"], SCOPE, REQUEST_HASH)).fetchone()
            audit(db, "receipt_read", values["handle"], "bound identity")
            return dict(row) if row else None
        elif action == "status":
            mode = values.get("mode", "pending")
            row = db.execute("SELECT * FROM executions WHERE handle=? AND scope=? AND request_hash=?",
                             (values["handle"], SCOPE, REQUEST_HASH)).fetchone()
            audit(db, "status_read", values["handle"], mode)
            if mode == "failed-read":
                return {"read_ok": False, "state": "unknown"}
            if mode == "stale-read":
                return {"read_ok": True, "state": "absent", "complete_lookup": False}
            return {"read_ok": True, "state": row["state"] if row else "absent", "handle": values["handle"]}
        elif action == "lookup":
            mode = values["mode"]
            audit(db, "content_lookup", detail=mode)
            if mode == "failed-read":
                return {"read_ok": False, "items": [], "complete_lookup": False}
            if mode == "delayed-visibility":
                return {"read_ok": True, "items": [], "complete_lookup": False}
            if mode == "wrong-receipt":
                row = db.execute("SELECT * FROM executions WHERE handle=?", (values["other_handle"],)).fetchone()
                return {"read_ok": True, "receipt": dict(row), "bound_to_original": False}
            query = "SELECT o.business,e.role FROM objects o JOIN executions e ON o.handle=e.handle"
            if mode == "identical-other":
                # The original is not visible yet, but a distinct identical
                # request is visible. A positive result is not original proof.
                query += " WHERE e.role='unrelated'"
            rows = [dict(row) for row in db.execute(query)]
            return {"read_ok": True, "items": rows, "complete_lookup": False,
                    "dedupe_active": db.execute("SELECT value FROM settings WHERE key='dedupe_active'").fetchone()[0] == "1"}
        elif action == "recover_identity":
            # This fixture explicitly grants a trusted operation index, unlike
            # a search over similar destination contents.
            rows = db.execute("SELECT * FROM executions WHERE role='original' AND scope=? AND request_hash=?",
                              (SCOPE, REQUEST_HASH)).fetchall()
            audit(db, "identity_recovery", detail="authoritative original-operation index")
            return dict(rows[0]) if len(rows) == 1 else None
        elif action == "cancelled":
            return db.execute("SELECT value FROM settings WHERE key='cancelled'").fetchone()[0] == "1"
        else:
            raise ValueError("unknown provider action")
    return {"ok": True}


def evidence():
    with contextlib.closing(sqlite3.connect(f"file:{STATE / 'destination.sqlite'}?mode=ro", uri=True)) as db:
        db.row_factory = sqlite3.Row
        return {table: [dict(row) for row in db.execute(f"SELECT * FROM {table}")]
                for table in ("executions", "history", "objects", "audit")}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("action")
    parser.add_argument("--values", default="{}")
    args = parser.parse_args()
    print(json.dumps(operation(args.action, **json.loads(args.values)), sort_keys=True))
