export type CursorHookDef = { command: string; timeout?: number };
type ClaudeHookCommand = { type?: string; command?: string; url?: string; [key: string]: unknown };
type ClaudeHookMatcher = { hooks?: ClaudeHookCommand[]; [key: string]: unknown };
export type ClaudeSettings = { hooks?: Record<string, ClaudeHookMatcher[]>; [key: string]: unknown };

function isSwarmCursorHook(command: string): boolean {
  return command.includes("post-hook.mjs") && /(?:^|\s)cursor(?:\s|$)/.test(command);
}

/** Remove legacy Swarm Cursor hooks without disturbing hooks installed by the user. */
export function removeSwarmCursorHooks(
  existing: { version?: number; hooks?: Record<string, CursorHookDef[]> },
): { version?: number; hooks: Record<string, CursorHookDef[]> } {
  const hooks: Record<string, CursorHookDef[]> = {};
  for (const [event, definitions] of Object.entries(existing.hooks ?? {})) {
    const kept = definitions.filter((definition) => !isSwarmCursorHook(definition.command));
    if (kept.length > 0) hooks[event] = kept;
  }
  return { ...existing, hooks };
}

function isSwarmClaudeHook(hook: ClaudeHookCommand): boolean {
  return Boolean(
    hook.url?.includes("/hooks/claude/") ||
      (hook.command?.includes("post-hook.mjs") && /(?:^|\s)claude(?:\s|$)/.test(hook.command)),
  );
}

/** Remove legacy Swarm hooks from Claude settings while preserving all unrelated settings. */
export function removeSwarmClaudeHooks(existing: ClaudeSettings): ClaudeSettings {
  const hooks: Record<string, ClaudeHookMatcher[]> = {};
  for (const [event, matchers] of Object.entries(existing.hooks ?? {})) {
    const kept = matchers.flatMap((matcher) => {
      if (!matcher.hooks) return [matcher];
      const commands = matcher.hooks.filter((hook) => !isSwarmClaudeHook(hook));
      return commands.length > 0 ? [{ ...matcher, hooks: commands }] : [];
    });
    if (kept.length > 0) hooks[event] = kept;
  }
  return { ...existing, hooks };
}
