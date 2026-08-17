import { createUserMessage } from "@deepseek-ai/dsh-llm";

import { runAgentMemory } from "./process.js";

export function extractUserPrompt(messages) {
  if (!Array.isArray(messages)) return "";
  return messages
    .filter((message) => message?.source?.kind === "user")
    .flatMap((message) => Array.isArray(message.content) ? message.content : [])
    .filter((block) => block?.type === "text" && typeof block.text === "string")
    .map((block) => block.text.trim())
    .filter(Boolean)
    .join("\n\n");
}

export function buildArguments(config) {
  const args = [
    "inject", "deepseek-harness",
    "--root", config.evidenceRoot,
    "--repo", config.portableRepo,
    "--limit", String(config.limit),
    "--token-budget", String(config.tokenBudget),
    "--byte-budget", String(config.byteBudget),
  ];
  appendScope(args, "--scope-repository", config.scopeRepository);
  appendScope(args, "--scope-project", config.scopeProject);
  appendScope(args, "--scope-task", config.scopeTask);
  return args;
}

export async function retrieveMemory(ctx, config, request) {
  const raw = await runAgentMemory(ctx, {
    binary: config.binary,
    args: buildArguments(config),
    cwd: request.cwd,
    input: {
      session_id: request.sessionId,
      cwd: request.cwd,
      hook_event_name: "UserPromptSubmit",
      turn_id: `${String(request.turn)}:${String(request.step)}`,
      prompt: request.prompt,
      source: "dsh-agent-pre-step",
    },
    timeoutMs: config.timeoutMs,
    stdoutMaxBytes: config.stdoutMaxBytes,
    stderrMaxBytes: config.stderrMaxBytes,
    signal: request.signal,
  });
  const output = JSON.parse(raw);
  if (!output || output.continue !== true) {
    throw new Error("Agent Memory returned an invalid continuation response");
  }
  const context = output.hookSpecificOutput?.additionalContext;
  if (context === undefined) return "";
  if (typeof context !== "string") {
    throw new Error("Agent Memory returned a non-text memory context");
  }
  if (Buffer.byteLength(context, "utf8") > config.byteBudget) {
    throw new Error(`Agent Memory context exceeded ${config.byteBudget} UTF-8 bytes`);
  }
  return context;
}

export function memoryMessage(context) {
  return createUserMessage({
    content: [{ type: "text", text: context }],
    source: {
      kind: "plugin",
      plugin: "agent-memory",
      form: "recall",
      summary: "Verified promoted Agent Memory",
    },
  });
}

function appendScope(args, flag, value) {
  if (typeof value === "string" && value.trim()) args.push(flag, value.trim());
}
