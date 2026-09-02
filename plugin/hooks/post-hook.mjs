#!/usr/bin/env node
/** Hook POST to swarmd; relays the daemon's JSON reply to stdout. Always exit 0 so hosts don't treat hook posts as failures. */
const platform = process.argv[2] ?? "claude";
const event = process.argv[3] ?? "unknown";
const url = process.env.SWARM_URL ?? "http://127.0.0.1:7777";

const fallback =
  platform === "antigravity" && (event === "PreToolUse" || event === "Stop")
    ? JSON.stringify({ decision: "allow" })
    : "{}";

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
  let reply = fallback;
  try {
    const res = await fetch(`${url}/hooks/${platform}/${event}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: AbortSignal.timeout(4000),
    });
    const text = await res.text();
    if (res.ok && text.trim()) {
      JSON.parse(text); // relay only valid JSON
      reply = text;
    }
  } catch {
    // daemon down, slow, or not returning JSON — fall back to the platform default
  }
  process.stdout.write(reply);
});
