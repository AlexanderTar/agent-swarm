#!/usr/bin/env node
/** Fire-and-forget hook POST to swarmd. Always exit 0 so hosts don't treat hook posts as failures. */
const platform = process.argv[2] ?? "claude";
const event = process.argv[3] ?? "unknown";
const url = process.env.SWARM_URL ?? "http://127.0.0.1:7777";

let body = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (c) => (body += c));
process.stdin.on("end", async () => {
  let payload = {};
  try {
    payload = body ? JSON.parse(body) : {};
  } catch {
    payload = {};
  }
  try {
    await fetch(`${url}/hooks/${platform}/${event}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: AbortSignal.timeout(4000),
    });
  } catch {
    // daemon down or slow — never fail the host hook
  }
  process.stdout.write("{}");
});
