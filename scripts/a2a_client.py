#!/usr/bin/env python3
"""Minimal A2A test client for Percy.

Usage:
  PERCY_A2A_TOKEN=...  ./scripts/a2a_client.py [--stream] [--url URL] PROMPT

Defaults to http://localhost:8080. Prints the agent card, then sends a single
message and prints the result (or streams events).
"""
import argparse
import json
import os
import sys
import urllib.request
import uuid


def rpc(url: str, token: str, method: str, params: dict, stream: bool = False):
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": str(uuid.uuid4()),
        "method": method,
        "params": params,
    }).encode()
    req = urllib.request.Request(
        url + "/a2a",
        data=body,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
    )
    resp = urllib.request.urlopen(req)
    if stream:
        for line in resp:
            line = line.decode().rstrip("\n")
            if line.startswith("data: "):
                yield json.loads(line[6:])
    else:
        yield json.loads(resp.read())


def get_card(url: str):
    req = urllib.request.Request(url + "/.well-known/agent-card.json")
    return json.loads(urllib.request.urlopen(req).read())


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://localhost:8080")
    ap.add_argument("--stream", action="store_true")
    ap.add_argument("--context", help="existing contextId (conversation)")
    ap.add_argument("prompt")
    args = ap.parse_args()

    token = os.environ.get("PERCY_A2A_TOKEN")
    if not token:
        sys.exit("PERCY_A2A_TOKEN env var required")

    print("== Agent card ==")
    card = get_card(args.url)
    print(json.dumps({k: card[k] for k in ("name", "version", "url", "capabilities")}, indent=2))

    message = {
        "kind": "message",
        "messageId": str(uuid.uuid4()),
        "role": "user",
        "parts": [{"kind": "text", "text": args.prompt}],
    }
    if args.context:
        message["contextId"] = args.context

    method = "message/stream" if args.stream else "message/send"
    print(f"\n== {method} ==")
    for event in rpc(args.url, token, method, {"message": message}, stream=args.stream):
        print(json.dumps(event, indent=2))


if __name__ == "__main__":
    main()
