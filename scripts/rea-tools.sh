#!/bin/sh
set -eu

# Print the names exposed by the pinned REA MCP server. Redirect this output to
# internal/rea/testdata/tools-6.5.0.txt when updating the release catalog.
exec node <<'NODE'
const { spawn } = require("node:child_process");

const child = spawn("npx", ["-y", "rea-agents@6.5.0", "mcp"], {
  stdio: ["pipe", "pipe", "inherit"],
});
let buffer = "";
let nextID = 1;
let requested = new Map();
let closed = false;
let timer = setTimeout(() => fail(new Error("timed out waiting for REA tools/list")), 180000);

function fail(error) {
  if (closed) return;
  closed = true;
  clearTimeout(timer);
  child.kill("SIGTERM");
  console.error(`REA tools/list failed: ${error.message}`);
  process.exitCode = 1;
}

child.on("error", fail);
child.on("close", (code) => {
  if (closed) return;
  if (code !== 0) fail(new Error(`server exited with status ${code}`));
  else fail(new Error("server exited before replying to tools/list"));
});
child.stdout.setEncoding("utf8");
child.stdout.on("data", (chunk) => {
  buffer += chunk;
  for (;;) {
    const end = buffer.indexOf("\n");
    if (end < 0) break;
    const line = buffer.slice(0, end).trim();
    buffer = buffer.slice(end + 1);
    if (!line) continue;
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      continue;
    }
    const resolve = requested.get(message.id);
    if (resolve) {
      requested.delete(message.id);
      if (message.error) {
        fail(new Error(message.error.message || JSON.stringify(message.error)));
      } else {
        resolve(message.result);
      }
    }
  }
});

function request(method, params) {
  const id = nextID++;
  return new Promise((resolve, reject) => {
    requested.set(id, resolve);
    child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id, method, params })}\n`, (error) => {
      if (error) {
        requested.delete(id);
        reject(error);
      }
    });
  });
}

function notify(method, params) {
  child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", method, params })}\n`);
}

(async () => {
  const init = await request("initialize", {
    protocolVersion: "2025-06-18",
    capabilities: {},
    clientInfo: { name: "blaze-rea-tools-catalog", version: "1.0.0" },
  });
  notify("notifications/initialized", {});

  const names = new Set();
  let cursor;
  do {
    const result = await request("tools/list", cursor ? { cursor } : {});
    for (const tool of result.tools || []) names.add(tool.name);
    cursor = result.nextCursor;
  } while (cursor);

  if (names.size === 0) throw new Error("tools/list returned no tools");
  closed = true;
  clearTimeout(timer);
  for (const name of [...names].sort()) console.log(name);
  child.kill("SIGTERM");
  void init;
})().catch(fail);
NODE
