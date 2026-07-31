"""
InfernoSIM example: Python FastAPI service
Run: uvicorn main:app --port 8081
"""
from fastapi import FastAPI, HTTPException, Header
from typing import Optional

app = FastAPI()
TOKENS: dict[str, str] = {}

@app.post("/login")
async def login(body: dict):
    token = f"tok_{body.get('user','anon')}_secret"
    TOKENS[token] = body.get("user", "anon")
    return {"access_token": token}

@app.get("/orders")
async def orders(authorization: Optional[str] = Header(None)):
    token = (authorization or "").removeprefix("Bearer ")
    if token not in TOKENS:
        raise HTTPException(status_code=401, detail="unauthorized")
    return {"orders": [{"id": "order_001", "status": "confirmed"}]}

@app.get("/health")
async def health():
    return {"status": "ok"}
