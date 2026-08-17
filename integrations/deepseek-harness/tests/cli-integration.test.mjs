import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import test from "node:test";

import { HarnessCapture } from "../lib/capture.js";

test("imports the Bundle capture spool through the real Agent Memory CLI", async (t) => {
  const binary = requiredEnvironment("AGENTMEM_BINARY");
  const base = process.env.AGENT_MEMORY_DSH_TEST_TMP || os.tmpdir();
  await mkdir(base, { recursive: true });
  const root = await mkdtemp(path.join(base, "dsh-agent-memory-cli-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const captureDir = path.join(root, "capture");
  const evidenceRoot = path.join(root, "evidence");

  const capture = new HarnessCapture(path.join(captureDir, "deepseek-harness-events.jsonl"));
  await capture.initialize();
  await capture.captureEvent(
    { id: "real-cli-session", header: { id: "real-cli-session", cwd: root } },
    {
      type: "user/message",
      seq: 0,
      time: Date.now(),
      data: {
        message: {
          role: "user",
          content: [{ type: "text", text: "synthetic public integration event" }],
          source: { kind: "user" },
        },
      },
    },
  );
  await capture.drain();

  await run(binary, ["init", "--root", evidenceRoot]);
  const imported = JSON.parse(await run(binary, [
    "import", "deepseek-harness-events", "--root", evidenceRoot, "--path", captureDir,
  ]));
  assert.equal(imported.schema_version, "agent-history-import-result/v1alpha1");
  assert.ok(imported.events_appended >= 1);
  assert.equal(imported.gaps_appended, 0);
  const doctor = JSON.parse(await run(binary, ["doctor", "--root", evidenceRoot]));
  assert.equal(doctor.ready, true);
  assert.deepEqual(doctor.issues, []);
});

function requiredEnvironment(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required for the real CLI integration test`);
  return value;
}

function run(executable, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      shell: false,
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(chunk));
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0 && signal === null) {
        resolve(Buffer.concat(stdout).toString("utf8"));
        return;
      }
      reject(new Error(
        `agentmem exited (code ${String(code)}, signal ${String(signal)}): ${Buffer.concat(stderr).toString("utf8")}`,
      ));
    });
  });
}
