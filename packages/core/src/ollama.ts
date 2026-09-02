import { z } from "zod";
import { zodToJsonSchema } from "zod-to-json-schema";
import type { SwarmConfig } from "./types.js";

export class OllamaClient {
  constructor(private config: SwarmConfig) {}

  async preflight(): Promise<{ ok: boolean; errors: string[] }> {
    const errors: string[] = [];
    try {
      const res = await fetch(`${this.config.ollamaUrl}/api/tags`, { signal: AbortSignal.timeout(5000) });
      if (!res.ok) {
        errors.push(`Ollama unreachable: ${res.status}`);
        return { ok: false, errors };
      }
      const data = (await res.json()) as { models?: Array<{ name: string }> };
      const names = new Set((data.models ?? []).map((m) => m.name.split(":")[0] ?? m.name));
      const embedBase = this.config.embedModel.split(":")[0] ?? this.config.embedModel;
      const chatBase = this.config.chatModel.split(":")[0] ?? this.config.chatModel;
      if (![...names].some((n) => n === embedBase || n.startsWith(embedBase))) {
        errors.push(`Missing embed model: ${this.config.embedModel}. Run: ollama pull ${this.config.embedModel}`);
      }
      if (![...names].some((n) => n === chatBase || n.startsWith(chatBase))) {
        errors.push(`Missing chat model: ${this.config.chatModel}. Run: ollama pull ${this.config.chatModel}`);
      }
    } catch (e) {
      errors.push(`Ollama unreachable at ${this.config.ollamaUrl}: ${e instanceof Error ? e.message : String(e)}`);
    }
    return { ok: errors.length === 0, errors };
  }

  async embed(inputs: string[]): Promise<Float32Array[]> {
    const res = await fetch(`${this.config.ollamaUrl}/api/embed`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model: this.config.embedModel,
        input: inputs,
        dimensions: this.config.embedDimensions,
        options: { num_ctx: 8192 },
      }),
      signal: AbortSignal.timeout(120_000),
    });
    if (!res.ok) throw new Error(`Ollama embed failed: ${res.status} ${await res.text()}`);
    const data = (await res.json()) as { embeddings: number[][] };
    return data.embeddings.map((e) => new Float32Array(e));
  }

  async embedOne(text: string): Promise<Float32Array> {
    const [vec] = await this.embed([text]);
    if (!vec) throw new Error("Empty embedding response");
    return vec;
  }

  async chat<T>(options: {
    system?: string;
    user: string;
    schema: z.ZodType<T>;
    schemaDescription?: string;
  }): Promise<T> {
    const jsonSchema = zodToJsonSchema(options.schema, { target: "openApi3" });
    const schemaHint = options.schemaDescription ?? "Respond with JSON matching the schema.";
    const res = await fetch(`${this.config.ollamaUrl}/api/chat`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model: this.config.chatModel,
        messages: [
          ...(options.system ? [{ role: "system", content: options.system }] : []),
          {
            role: "user",
            content: `${options.user}\n\n${schemaHint}`,
          },
        ],
        stream: false,
        format: jsonSchema,
        options: { temperature: 0 },
        keep_alive: "30m",
      }),
      signal: AbortSignal.timeout(300_000),
    });
    if (!res.ok) throw new Error(`Ollama chat failed: ${res.status} ${await res.text()}`);
    const data = (await res.json()) as { message?: { content?: string } };
    const content = data.message?.content;
    if (!content) throw new Error("Empty chat response");
    const parsed = JSON.parse(content) as unknown;
    return options.schema.parse(parsed);
  }
}
