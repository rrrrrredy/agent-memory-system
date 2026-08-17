import assert from "node:assert/strict";
import test from "node:test";

import { runAgentMemory } from "../lib/process.js";

function reader(state, key) {
  return {
    readFrom() {
      const text = state[key] || "";
      return {
        text,
        nextOffset: Buffer.byteLength(text),
        lossy: Boolean(state[`${key}Lossy`]),
      };
    },
  };
}

function fakeContext(handler, resolver = async (command) => command) {
  return {
    subprocess: {
      resolveExecutable: resolver,
      spawn(spec) {
        const state = {};
        const done = Promise.resolve(handler(spec)).then((result) => {
          Object.assign(state, result);
          return { exitCode: result.exitCode ?? 0, signal: result.signal ?? null };
        });
        return {
          collected: { stdout: reader(state, "stdout"), stderr: reader(state, "stderr") },
          done,
        };
      },
    },
  };
}

const base = {
  binary: "agentmem",
  args: ["inject", "deepseek-harness"],
  cwd: process.cwd(),
  input: { session_id: "s", prompt: "remember" },
  timeoutMs: 1000,
  stdoutMaxBytes: 1024,
  stderrMaxBytes: 1024,
};

test("uses resolved argv and structured stdin without a shell", async () => {
  let captured;
  const result = await runAgentMemory(fakeContext((spec) => {
    captured = spec;
    return { stdout: "{}", stderr: "", exitCode: 0 };
  }), base);
  assert.equal(result, "{}");
  assert.deepEqual(captured.argv, ["agentmem", "inject", "deepseek-harness"]);
  assert.deepEqual(JSON.parse(captured.stdio.stdin.data), base.input);
  assert.equal("shell" in captured, false);
});

test("rejects missing executables, nonzero exits, and bounded-output loss", async () => {
  await assert.rejects(
    runAgentMemory(fakeContext(() => ({}), async () => { throw new Error("missing"); }), base),
    /could not run/,
  );
  await assert.rejects(
    runAgentMemory(fakeContext(() => ({ stdout: "", stderr: "bad", exitCode: 7 })), base),
    /code 7.*bad/,
  );
  await assert.rejects(
    runAgentMemory(fakeContext(() => ({ stdout: "tail", stderr: "", stdoutLossy: true })), base),
    /stdout exceeded 1024 bytes/,
  );
  await assert.rejects(
    runAgentMemory(fakeContext(() => ({ stdout: "", stderr: "tail", stderrLossy: true })), base),
    /stderr exceeded 1024 bytes/,
  );
});

test("classifies a hung subprocess as a timeout", async () => {
  const ctx = fakeContext((spec) => new Promise((resolve) => {
    spec.signal.addEventListener(
      "abort",
      () => resolve({ stdout: "", stderr: "", exitCode: null, signal: "SIGTERM" }),
      { once: true },
    );
  }));
  await assert.rejects(runAgentMemory(ctx, { ...base, timeoutMs: 20 }), /timed out after 20ms/);
});
