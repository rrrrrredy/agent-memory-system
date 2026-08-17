import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { CAPTURE_SCHEMA, HarnessCapture } from "../lib/capture.js";

async function scratch(t) {
  const base = process.env.AGENT_MEMORY_DSH_TEST_TMP || os.tmpdir();
  await mkdir(base, { recursive: true });
  const root = await mkdtemp(path.join(base, "dsh-agent-memory-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

async function records(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const result = [];
  for (const entry of entries) {
    if (!entry.isFile() || !entry.name.endsWith(".jsonl") || entry.name.endsWith(".partial.jsonl")) continue;
    const text = await readFile(path.join(directory, entry.name), "utf8");
    for (const line of text.split("\n")) {
      if (line.trim()) result.push(JSON.parse(line));
    }
  }
  return result;
}

test("durably preserves unknown live events without projecting them away", async (t) => {
  const root = await scratch(t);
  const capture = new HarnessCapture(path.join(root, "deepseek-harness-events.jsonl"));
  await capture.initialize();
  const event = { type: "future/event", seq: 9, time: 123, data: { nested: ["exact"] } };
  await capture.captureEvent({ id: "session-a", header: { id: "session-a", cwd: root } }, event);
  await capture.drain();
  const output = await records(root);
  assert.equal(output.length, 1);
  assert.equal(output[0].schema_version, CAPTURE_SCHEMA);
  assert.deepEqual(output[0].event, event);
});

test("backfills verbatim raw artifacts exactly once across restarts", async (t) => {
  const root = await scratch(t);
  const destination = path.join(root, "deepseek-harness-events.jsonl");
  const artifact = '{"type":"session","id":"session-a"}\n{"type":"future/event","seq":0}\n';
  const persistence = {
    supportsRawArtifacts: true,
    async listSnapshots() { return [{ header: { id: "session-a", cwd: root }, revision: "rev-1" }]; },
    async readRaw() { return { meta: { id: "session-a", cwd: root }, filename: "session.jsonl", content: artifact }; },
  };
  for (let run = 0; run < 2; run++) {
    const capture = new HarnessCapture(destination);
    await capture.initialize();
    await capture.backfill(persistence);
    await capture.drain();
  }
  const output = (await records(root)).filter((record) => record.kind === "raw_artifact");
  assert.equal(output.length, 1);
  assert.equal(output[0].artifact.content, artifact);
  assert.equal(output[0].revision, "rev-1");
});

test("records an explicit gap when raw persistence artifacts are unavailable", async (t) => {
  const root = await scratch(t);
  const capture = new HarnessCapture(path.join(root, "deepseek-harness-events.jsonl"));
  await capture.initialize();
  await capture.backfill({ supportsRawArtifacts: false });
  await capture.drain();
  assert.ok((await records(root)).some((record) =>
    record.kind === "gap" && record.reason_code === "raw_artifacts_unsupported"));
});

test("rejects any raw capture destination inside a Git worktree", async (t) => {
  const root = await scratch(t);
  await mkdir(path.join(root, ".git"));
  const capture = new HarnessCapture(path.join(root, "evidence", "deepseek-harness-events.jsonl"));
  await assert.rejects(capture.initialize(), /outside a Git worktree/);
});

test("turns a twice-failed append into an explicit record-preserving gap", async (t) => {
  const root = await scratch(t);
  const stored = [];
  let calls = 0;
  const spool = {
    async initialize() {},
    async append(record) {
      calls += 1;
      if (calls <= 2) throw new Error(`write-${calls}`);
      stored.push(record);
    },
    async drain() {},
  };
  const capture = new HarnessCapture(path.join(root, "deepseek-harness-events.jsonl"), { spool });
  await capture.initialize();
  const record = { schema_version: CAPTURE_SCHEMA, kind: "session_event", session: { id: "s" }, event: { type: "x" } };
  await capture.append(record);
  assert.equal(stored[0].kind, "gap");
  assert.equal(stored[0].reason_code, "capture_append_failed");
  assert.deepEqual(stored[0].failed_record, record);
});

test("build output is byte-identical to the canonical crash-safe spool", async () => {
  const integrationRoot = fileURLToPath(new URL("../", import.meta.url));
  const repositoryRoot = path.resolve(integrationRoot, "..", "..");
  const manifest = JSON.parse(await readFile(path.join(integrationRoot, "source-integrity.json"), "utf8"));
  const canonical = await readFile(path.join(repositoryRoot, "integrations", "opencode", "crash-safe-jsonl.mjs"));
  const bundled = await readFile(path.join(integrationRoot, "lib", "crash-safe-jsonl.js"));
  const digest = (value) => createHash("sha256").update(value).digest("hex");
  assert.equal(digest(canonical), digest(bundled));
  assert.equal(manifest.files[0].sha256, digest(canonical));
});

test("published package metadata matches the bundled repository license", async () => {
  const integrationRoot = fileURLToPath(new URL("../", import.meta.url));
  const metadata = JSON.parse(await readFile(path.join(integrationRoot, "package.json"), "utf8"));
  const license = await readFile(path.join(integrationRoot, "LICENSE"), "utf8");
  assert.equal(metadata.license, "MIT");
  assert.match(license, /^MIT License\r?\n/);
});
