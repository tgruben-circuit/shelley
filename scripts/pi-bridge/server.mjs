#!/usr/bin/env node
// pi-bridge: expose pi-coding-agent (https://github.com/earendil-works/pi) as
// an A2A-protocol HTTP server so Percy (or any A2A client) can dispatch tasks
// to a remote pi instance.
//
// Usage:
//   PI_BRIDGE_TOKEN=secret PI_BRIDGE_PORT=9000 PI_BIN=pi node server.mjs
//
// Each message/send spawns `pi --mode json --no-session <prompt>` in PI_CWD
// (defaults to $PWD), accumulates assistant text from the JSONL event stream,
// and returns it as an A2A Task. v1 is one-shot: contextId is accepted but not
// used for session resumption.
import http from "node:http";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import readline from "node:readline";

const PORT = Number(process.env.PI_BRIDGE_PORT || 9000);
const TOKEN = process.env.PI_BRIDGE_TOKEN || "";
const PI_BIN = process.env.PI_BIN || "pi";
const PI_CWD = process.env.PI_CWD || process.cwd();
const PUBLIC_URL = process.env.PI_BRIDGE_URL || `http://localhost:${PORT}/a2a`;

if (!TOKEN) {
  console.error("PI_BRIDGE_TOKEN is required");
  process.exit(1);
}

const card = {
  protocolVersion: "0.2.5",
  name: "pi-coding-agent",
  description: "pi-coding-agent (earendil-works/pi) fronted by an A2A bridge.",
  url: PUBLIC_URL,
  version: "0.1.0",
  capabilities: { streaming: false, pushNotifications: false },
  defaultInputModes: ["text/plain"],
  defaultOutputModes: ["text/plain"],
  skills: [{ id: "coding", name: "Coding tasks", description: "read/write/edit/bash via pi", tags: ["code", "shell"] }],
  securitySchemes: { bearer: { type: "http", scheme: "bearer" } },
  security: [{ bearer: [] }],
};

function send(res, status, body, headers = {}) {
  res.writeHead(status, { "Content-Type": "application/json", ...headers });
  res.end(JSON.stringify(body));
}

function rpcError(id, code, message) {
  return { jsonrpc: "2.0", id, error: { code, message } };
}

function rpcResult(id, result) {
  return { jsonrpc: "2.0", id, result };
}

async function runPi(prompt) {
  return new Promise((resolve, reject) => {
    const args = ["--mode", "json", "--no-session", prompt];
    const child = spawn(PI_BIN, args, { cwd: PI_CWD, env: process.env });
    let assistantText = [];
    let stderr = "";
    const rl = readline.createInterface({ input: child.stdout });
    rl.on("line", (line) => {
      let ev;
      try { ev = JSON.parse(line); } catch { return; }
      if (ev.type === "message_end" && ev.message?.role === "assistant") {
        for (const part of ev.message.content || []) {
          if (part.type === "text" && part.text) assistantText.push(part.text);
        }
      }
    });
    child.stderr.on("data", (chunk) => { stderr += chunk.toString(); });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code !== 0) {
        reject(new Error(`pi exited ${code}: ${stderr.slice(-500)}`));
      } else {
        resolve(assistantText.join("\n\n"));
      }
    });
  });
}

function extractPrompt(msg) {
  if (!msg || msg.role !== "user") throw new Error("message.role must be 'user'");
  if (!Array.isArray(msg.parts) || msg.parts.length === 0) throw new Error("message has no parts");
  const texts = [];
  for (const p of msg.parts) {
    if (p.kind !== "text") throw new Error(`unsupported part kind: ${p.kind}`);
    texts.push(p.text || "");
  }
  return texts.join("\n\n");
}

const server = http.createServer(async (req, res) => {
  if (req.method === "GET" && req.url === "/.well-known/agent-card.json") {
    send(res, 200, card);
    return;
  }
  if (req.url !== "/a2a") {
    send(res, 404, { error: "not found" });
    return;
  }
  if (req.headers.authorization !== `Bearer ${TOKEN}`) {
    send(res, 401, { error: "unauthorized" });
    return;
  }
  if (req.method !== "POST") {
    send(res, 405, { error: "method not allowed" });
    return;
  }
  const chunks = [];
  for await (const c of req) chunks.push(c);
  let body;
  try { body = JSON.parse(Buffer.concat(chunks).toString()); }
  catch (e) { send(res, 200, rpcError(null, -32700, "parse error: " + e.message)); return; }

  const { id, method, params } = body;
  try {
    if (method === "message/send") {
      const prompt = extractPrompt(params?.message);
      const contextId = params?.message?.contextId || `pi-ctx-${randomUUID()}`;
      const taskId = `task-${randomUUID()}`;
      console.log(`[pi-bridge] message/send context=${contextId} prompt=${prompt.slice(0, 80)}`);
      const reply = await runPi(prompt);
      const task = {
        kind: "task",
        id: taskId,
        contextId,
        status: {
          state: "completed",
          timestamp: new Date().toISOString(),
          message: {
            kind: "message",
            messageId: `msg-${randomUUID()}`,
            role: "agent",
            parts: [{ kind: "text", text: reply }],
            contextId,
            taskId,
          },
        },
      };
      send(res, 200, rpcResult(id, task));
    } else {
      send(res, 200, rpcError(id, -32601, `method not supported: ${method}`));
    }
  } catch (e) {
    console.error(`[pi-bridge] error:`, e);
    send(res, 200, rpcError(id, -32603, e.message));
  }
});

server.listen(PORT, () => {
  console.log(`[pi-bridge] listening on :${PORT}, pi=${PI_BIN}, cwd=${PI_CWD}`);
});
