import assert from "node:assert/strict";
import test from "node:test";

import { createUserMessage } from "@deepseek-ai/dsh-llm";

import { buildArguments, extractUserPrompt, memoryMessage, retrieveMemory } from "../lib/retrieval.js";

const config = {
  binary: "agentmem",
  evidenceRoot: "/evidence",
  portableRepo: "/portable",
  scopeRepository: "repo",
  scopeProject: "project",
  scopeTask: "task",
  limit: 4,
  tokenBudget: 500,
  byteBudget: 128,
  timeoutMs: 1000,
  stdoutMaxBytes: 4096,
  stderrMaxBytes: 4096,
};

function contextFor(stdout) {
  return {
    subprocess: {
      async resolveExecutable(command) { return command; },
      spawn(spec) {
        return {
          done: Promise.resolve({ exitCode: 0, signal: null }),
          collected: {
            stdout: { readFrom: () => ({ text: stdout, lossy: false, nextOffset: stdout.length }) },
            stderr: { readFrom: () => ({ text: "", lossy: false, nextOffset: 0 }) },
          },
          spec,
        };
      },
    },
  };
}

test("extracts only user-originated text", () => {
  const messages = [
    createUserMessage({ content: [{ type: "text", text: "first" }], source: { kind: "user" } }),
    createUserMessage({
      content: [{ type: "text", text: "do not feed back" }],
      source: { kind: "plugin", plugin: "agent-memory", form: "recall" },
    }),
    createUserMessage({ content: [{ type: "text", text: "second" }], source: { kind: "user" } }),
  ];
  assert.equal(extractUserPrompt(messages), "first\n\nsecond");
});

test("builds the native deepseek-harness injection argv", () => {
  assert.deepEqual(buildArguments(config), [
    "inject", "deepseek-harness", "--root", "/evidence", "--repo", "/portable",
    "--limit", "4", "--token-budget", "500", "--byte-budget", "128",
    "--scope-repository", "repo", "--scope-project", "project", "--scope-task", "task",
  ]);
});

test("accepts only a valid bounded continuation response", async () => {
  const request = {
    sessionId: "session", cwd: process.cwd(), turn: 2, step: 3,
    prompt: "query", signal: new AbortController().signal,
  };
  const valid = JSON.stringify({
    continue: true,
    hookSpecificOutput: { additionalContext: "verified memory" },
  });
  assert.equal(await retrieveMemory(contextFor(valid), config, request), "verified memory");
  await assert.rejects(retrieveMemory(contextFor("{broken"), config, request), SyntaxError);
  await assert.rejects(
    retrieveMemory(contextFor(JSON.stringify({ continue: false })), config, request),
    /invalid continuation/,
  );
  await assert.rejects(
    retrieveMemory(contextFor(JSON.stringify({
      continue: true,
      hookSpecificOutput: { additionalContext: "x".repeat(129) },
    })), config, request),
    /exceeded 128 UTF-8 bytes/,
  );
});

test("marks injected context as a formal plugin recall message", () => {
  const message = memoryMessage("verified memory");
  assert.equal(message.source.kind, "plugin");
  assert.equal(message.source.plugin, "agent-memory");
  assert.equal(message.source.form, "recall");
  assert.equal(message.content[0].text, "verified memory");
});
