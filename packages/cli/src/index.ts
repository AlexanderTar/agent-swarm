#!/usr/bin/env node
import {
  runDoctor,
  runInstall,
  runStatus,
  runUninstall,
  runUpdate,
  pluginSync,
  type InstallOptions,
} from "./commands.js";

const [cmd, ...rest] = process.argv.slice(2);

function parseInstallOptions(args: string[]): InstallOptions {
  const opts: InstallOptions = {};
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === "--from-bootstrap") opts.fromBootstrap = true;
    else if (arg === "--yes" || arg === "-y") opts.yes = true;
    else if (arg === "--no-auto-update") opts.autoUpdate = false;
    else if (arg === "--no-pull-models") opts.pullModels = false;
    else if (arg === "--port" && args[i + 1]) {
      opts.port = Number(args[++i]);
    } else if (arg === "--agents" && args[i + 1]) {
      opts.agents = args[++i]!.split(",").map((s) => s.trim()).filter(Boolean);
    }
  }
  return opts;
}

async function main(): Promise<void> {
  switch (cmd) {
    case "install":
      await runInstall(parseInstallOptions(rest));
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
  swarm install       Interactive setup wizard (--yes for non-interactive)
  swarm doctor        Health checks (--hooks for synthetic replay)
  swarm status        Daemon status
  swarm update        Update to latest tag (--to vX.Y.Z)
  swarm uninstall     Remove launchd jobs
  swarm open          Open board in browser
  swarm plugin sync   Rewire agent plugin symlinks

Install flags:
  --yes, -y           Skip prompts (auto-detected when stdin is not a TTY)
  --from-bootstrap    Installed via install.sh (release already at ~/.swarm/app/current)
  --agents a,b,c      Agent ids: cursor, claude, codex, antigravity
  --port 7777         Daemon port
  --no-auto-update    Disable hourly updater launchd job
  --no-pull-models    Skip ollama pull for missing models
`);
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
