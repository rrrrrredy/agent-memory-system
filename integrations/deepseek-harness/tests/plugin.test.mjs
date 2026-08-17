import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { Context } from "@deepseek-ai/cordis";
import { agentEvents } from "@deepseek-ai/dsh-agent";
import { createUserMessage } from "@deepseek-ai/dsh-llm";
import SessionStore, { SessionId } from "@deepseek-ai/dsh-session";
import JsonlSessionPersistence from "@deepseek-ai/dsh-session-persistence-jsonl";
import { SubprocessRuntime } from "@deepseek-ai/dsh-subprocess";

import * as AgentMemoryBundle from "../lib/index.js";

function collected(state, key) {
  return {
    readFrom() {
      const text = state[key] || "";
      return { text, nextOffset: Buffer.byteLength(text), lossy: false };
    },
  };
}

function scriptedSubprocess(calls) {
  return class ScriptedSubprocess extends SubprocessRuntime {
    constructor(ctx) { super(ctx); }
    async resolveExecutable(command) { return command; }
    spawn(spec) {
      calls.push(spec);
      const state = {};
      const done = Promise.resolve().then(() => {
        const input = JSON.parse(spec.stdio.stdin.data);
        Object.assign(state, {
          stdout: JSON.stringify({
            continue: true,
            hookSpecificOutput: { additionalContext: `verified:${input.prompt}` },
          }),
          stderr: "",
        });
        return { exitCode: 0, signal: null };
      });
      return {
        collected: { stdout: collected(state, "stdout"), stderr: collected(state, "stderr") },
        done,
      };
    }
    async spawnTerminal() { throw new Error("not implemented"); }
  };
}

async function captureRecords(directory) {
  const output = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (!entry.isFile() || !entry.name.endsWith(".jsonl") || entry.name.endsWith(".partial.jsonl")) continue;
    for (const line of (await readFile(path.join(directory, entry.name), "utf8")).split("\n")) {
      if (line.trim()) output.push(JSON.parse(line));
    }
  }
  return output;
}

test("composes with real Cordis, SessionStore, JSONL persistence, waterfall, and disposal", async (t) => {
  const base = process.env.AGENT_MEMORY_DSH_TEST_TMP || os.tmpdir();
  await mkdir(base, { recursive: true });
  const root = await mkdtemp(path.join(base, "dsh-agent-memory-composition-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const persistenceRoot = path.join(root, "sessions");
  const captureRoot = path.join(root, "capture");
  const evidenceRoot = path.join(root, "evidence");
  const portableRepo = path.join(root, "portable");
  await Promise.all([mkdir(evidenceRoot), mkdir(portableRepo)]);

  const ctx = new Context();
  const sessionsFiber = ctx.plugin(SessionStore);
  await sessionsFiber.await();
  const persistenceFiber = ctx.plugin(JsonlSessionPersistence, {
    root: persistenceRoot,
    compression: "none",
    packChunks: false,
  });
  await persistenceFiber.await();
  const calls = [];
  const subprocessFiber = ctx.plugin(scriptedSubprocess(calls));
  await subprocessFiber.await();

  const session = ctx.sessions.create(SessionId("dsh-agent-memory-test"), { meta: { cwd: root } });
  const userMessage = createUserMessage({
    content: [{ type: "text", text: "remember the constraint" }],
    source: { kind: "user" },
  });
  session.append("user/message", userMessage, { surfaceOp: "append" });
  await ctx.sessions.flush(session);

  const bundleFiber = ctx.plugin(AgentMemoryBundle, {
    captureDir: captureRoot,
    evidenceRoot,
    portableRepo,
    timeoutMs: 1000,
    stdoutMaxBytes: 4096,
    stderrMaxBytes: 4096,
  });
  await bundleFiber.await();

  session.append("turn/start", { turn: 1 });
  await ctx.sessions.flush(session);
  const pluginInput = createUserMessage({
    content: [{ type: "text", text: "must not become query input" }],
    source: { kind: "plugin", plugin: "other", form: "notice" },
  });
  const agent = { session };
  const signal = new AbortController().signal;
  const decision = await agentEvents(ctx, agent).waterfall(
    "agent/pre-step",
    { messages: [userMessage, pluginInput], turn: 1, step: 1, signal },
    () => Promise.resolve({ kind: "enter", messages: [userMessage] }),
  );
  assert.equal(decision.kind, "enter");
  assert.equal(decision.messages.length, 2);
  assert.equal(decision.messages[1].source.plugin, "agent-memory");
  assert.equal(decision.messages[1].source.form, "recall");
  const invocation = JSON.parse(calls[0].stdio.stdin.data);
  assert.equal(invocation.prompt, "remember the constraint");
  assert.equal(invocation.hook_event_name, "UserPromptSubmit");
  assert.equal(invocation.turn_id, "1:1");

  await agentEvents(ctx, agent).waterfall(
    "agent/pre-step",
    { messages: [userMessage], turn: 1, step: 2, signal },
    () => Promise.resolve({ kind: "enter", messages: [userMessage] }),
  );
  assert.equal(calls.length, 1, "one turn must not receive duplicate memory injections");

  const beforeDispose = await captureRecords(captureRoot);
  assert.ok(beforeDispose.some((record) => record.kind === "raw_artifact" &&
    record.artifact.content.includes("remember the constraint")));
  assert.ok(beforeDispose.some((record) => record.kind === "session_event" &&
    record.event.type === "turn/start"));

  await bundleFiber.dispose();
  const callsBeforeRemoval = calls.length;
  await agentEvents(ctx, agent).waterfall(
    "agent/pre-step",
    { messages: [userMessage], turn: 2, step: 1, signal },
    () => Promise.resolve({ kind: "enter", messages: [userMessage] }),
  );
  assert.equal(calls.length, callsBeforeRemoval);
  session.append("turn/end", { turn: 1, reason: "completed" });
  await ctx.sessions.flush(session);
  assert.equal((await captureRecords(captureRoot)).length, beforeDispose.length);

  await subprocessFiber.dispose();
  await persistenceFiber.dispose();
  await sessionsFiber.dispose();
});
