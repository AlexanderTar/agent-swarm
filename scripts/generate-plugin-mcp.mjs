#!/usr/bin/env node
/**
 * Generate mcp.json, .mcp.json, and mcp_config.json from a single source.
 */
import { writeFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const root = join(__dirname, "..");
const pluginDir = join(root, "plugin");

const mcpConfig = {
  $schema: "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  mcpServers: {
    swarm: {
      type: "stdio",
      command: "node",
      args: ["bin/swarm-mcp.mjs"],
      env: {
        SWARM_URL: "http://127.0.0.1:7777",
        SWARM_REGISTER_SESSION: "0",
      },
    },
  },
};

mkdirSync(pluginDir, { recursive: true });
const json = `${JSON.stringify(mcpConfig, null, 2)}\n`;
writeFileSync(join(pluginDir, "mcp.json"), json);
writeFileSync(join(pluginDir, ".mcp.json"), json);
writeFileSync(join(pluginDir, "mcp_config.json"), json);
console.log("Generated plugin MCP configs");
