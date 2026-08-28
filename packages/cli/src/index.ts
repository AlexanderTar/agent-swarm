#!/usr/bin/env node
import {
  runDoctor,
  runInstall,
  runStatus,
  runUninstall,
  runUpdate,
  pluginSync,
} from "./commands.js";

const [cmd, ...rest] = process.argv.slice(2);

async function main(): Promise<void> {
  switch (cmd) {
    case "install":
      await runInstall(rest.includes("--from-bootstrap"));
      break;
    case "doctor":
      process.exit(await runDoctor(rest.includes("--hooks")));
    case "status":
      runStatus();
      break;
    case "update":
      await runUpdate({ to: rest.find((a) => !a.startsWith("-")), quiet: rest.includes("--quiet") });
      break;
    case "uninstall":
      await runUninstall();
      break;
    case "open": {
      const { execSync } = await import("node:child_process");
      execSync("open http://127.0.0.1:7777");
      break;
    }
    case "plugin":
      if (rest[0] === "sync") {
        pluginSync(["cursor", "claude", "codex", "antigravity"]);
        console.log("Plugin sync complete");
      }
      break;
    default:
      console.log(`swarm — Agent Swarm CLI

Usage:
  swarm install       Interactive setup wizard
  swarm doctor        Health checks (--hooks for synthetic replay)
  swarm status        Daemon status
  swarm update        Update to latest tag (--to vX.Y.Z)
  swarm uninstall     Remove launchd jobs
  swarm open          Open board in browser
  swarm plugin sync   Rewire agent plugin symlinks
`);
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
