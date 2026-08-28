#!/usr/bin/env node
/** Fire-and-forget hook POST to swarmd */
const platform = process.argv[2] ?? "claude";
const event = process.argv[3] ?? "unknown";
const url = process.env.SWARM_URL ?? "http://127.0.0.1:7777";

let body = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (c) => (body += c));
process.stdin.on("end", () => {
  const payload = body ? JSON.parse(body) : {};
  fetch(`${url}/hooks/${platform}/${event}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  }).catch(() => {});
  process.stdout.write("{}");
});
