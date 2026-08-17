import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import {
  access,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const PACKAGE_NAME = "@rrrrrredy/dsh-agent-memory";
const FINAL_TEXT_ONE = "DETERMINISTIC_DSH_AGENT_MEMORY_OK_1";
const FINAL_TEXT_TWO = "DETERMINISTIC_DSH_AGENT_MEMORY_OK_2";
const AFTER_REMOVE_TEXT = "DETERMINISTIC_DSH_AGENT_MEMORY_REMOVED";
const repositoryRoot = fileURLToPath(new URL("../../../", import.meta.url));
const fixtureRoot = path.join(repositoryRoot, "examples", "quickstart");
const fixtureManifest = JSON.parse(
  await readFile(path.join(fixtureRoot, "manifest.json"), "utf8"),
);
const taskText = fixtureManifest.retrieval_query;

function requiredPath(name) {
  const value = process.env[name];
  if (!value?.trim() || !path.isAbsolute(value)) {
    throw new Error(`${name} must be an absolute path`);
  }
  return path.normalize(value);
}

const dshEntry = requiredPath("DSH_ENTRY");
const agentmem = requiredPath("AGENTMEM_BINARY");
const scratchRoot = requiredPath("DSH_E2E_ROOT");
const packageSpec = process.env.DSH_PACKAGE_SPEC?.trim();
const tarball = process.env.DSH_TARBALL?.trim();
if (Boolean(packageSpec) === Boolean(tarball)) {
  throw new Error("exactly one of DSH_PACKAGE_SPEC or DSH_TARBALL must be set");
}
const installSpec = packageSpec || requiredPath("DSH_TARBALL");
const keepArtifacts = process.env.DSH_E2E_KEEP === "1";

await Promise.all([
  access(dshEntry),
  access(agentmem),
  ...(packageSpec ? [] : [access(installSpec)]),
]);
await mkdir(scratchRoot, { recursive: true });
const root = await mkdtemp(path.join(scratchRoot, "dsh-agent-memory-"));
const home = path.join(root, "home");
const workspace = path.join(root, "workspace");
const captureRoot = path.join(root, "capture");
const evidenceRoot = path.join(root, "evidence");
const portableRepo = path.join(root, "portable-memory");
const importEvidenceRoot = path.join(root, "imported-evidence");
await Promise.all([mkdir(home), mkdir(workspace)]);

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd ?? workspace,
      env: { ...process.env, ...options.env },
      shell: false,
      stdio: [options.input === undefined ? "ignore" : "pipe", "pipe", "pipe"],
      windowsHide: true,
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(Buffer.from(chunk)));
    child.stderr.on("data", (chunk) => stderr.push(Buffer.from(chunk)));
    child.once("error", reject);
    if (options.input !== undefined) child.stdin.end(options.input);
    const timeoutMs = options.timeoutMs ?? 60_000;
    const timer = setTimeout(() => {
      child.kill();
      reject(new Error(`${path.basename(command)} timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    child.once("close", (code, signal) => {
      clearTimeout(timer);
      resolve({
        code: code ?? -1,
        signal,
        stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
      });
    });
  });
}

async function runAgentmem(args, input) {
  const result = await run(agentmem, args, { input });
  assert.equal(result.code, 0, result.stderr || result.stdout);
  return JSON.parse(result.stdout);
}

function runDsh(args, extraEnv = {}, timeoutMs = 60_000) {
  return run(process.execPath, [dshEntry, ...args], {
    cwd: workspace,
    env: {
      DSH_HOME: home,
      DSH_TELEMETRY_MODE: "DISABLED",
      ...extraEnv,
    },
    timeoutMs,
  });
}

function beginStream(response) {
  response.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache",
    connection: "keep-alive",
  });
}

function sse(response, payload) {
  response.write(`data: ${typeof payload === "string" ? payload : JSON.stringify(payload)}\n\n`);
}

function finishText(response, text) {
  beginStream(response);
  sse(response, { choices: [{ index: 0, delta: { content: text }, finish_reason: null }] });
  sse(response, {
    choices: [{ index: 0, delta: { content: "" }, finish_reason: "stop" }],
    usage: { prompt_tokens: 3, completion_tokens: text.length },
  });
  sse(response, "[DONE]");
  response.end();
}

async function readBody(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(Buffer.from(chunk));
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

function allStrings(value, output = []) {
  if (typeof value === "string") output.push(value);
  else if (Array.isArray(value)) for (const item of value) allStrings(item, output);
  else if (value && typeof value === "object") {
    for (const item of Object.values(value)) allStrings(item, output);
  }
  return output;
}

async function filesUnder(directory) {
  const output = [];
  async function walk(current) {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const candidate = path.join(current, entry.name);
      if (entry.isDirectory()) await walk(candidate);
      else if (entry.isFile()) output.push(candidate);
    }
  }
  await walk(directory);
  return output;
}

async function readJsonlFiles(directory, predicate = () => true) {
  const records = [];
  const files = (await filesUnder(directory)).filter(
    (file) => file.endsWith(".jsonl") && !file.endsWith(".partial.jsonl") && predicate(file),
  );
  for (const file of files) {
    for (const line of (await readFile(file, "utf8")).split(/\r?\n/).filter(Boolean)) {
      records.push(JSON.parse(line));
    }
  }
  return { files, records };
}

function pluginRecall(record) {
  let found = false;
  function visit(value) {
    if (found || !value || typeof value !== "object") return;
    if (
      value.source?.kind === "plugin" &&
      value.source?.plugin === "agent-memory" &&
      value.source?.form === "recall"
    ) {
      found = true;
      return;
    }
    for (const child of Object.values(value)) visit(child);
  }
  visit(record);
  return found;
}

function yamlPath(value) {
  return JSON.stringify(value.replaceAll("\\", "/"));
}

function profilePatch() {
  return [
    "- id: session-title-llm",
    "  disabled: true",
    "- id: session-persistence-jsonl",
    "  config:",
    "    root: !!js dshHomePath('sessions')",
    "    compression: none",
    "- id: agent-memory",
    "  config:",
    `    binary: ${yamlPath(agentmem)}`,
    `    captureDir: ${yamlPath(captureRoot)}`,
    `    evidenceRoot: ${yamlPath(evidenceRoot)}`,
    `    portableRepo: ${yamlPath(portableRepo)}`,
    "    scopeProject: example-project",
    "    limit: 5",
    "    tokenBudget: 600",
    "    byteBudget: 3072",
    "    timeoutMs: 10000",
    "    stdoutMaxBytes: 131072",
    "    stderrMaxBytes: 131072",
    "    segmentMaxBytes: 33554432",
    "",
  ].join("\n");
}

async function seedVerifiedMemory() {
  const onboard = await runAgentmem([
    "onboard", "codex", "--root", evidenceRoot, "--path", fixtureRoot,
  ]);
  assert.equal(onboard.import.gaps_appended, 0);
  assert.equal(onboard.doctor.ready, true);
  assert.equal(onboard.candidates.review_ready, fixtureManifest.expected_review_ready);

  const queue = await runAgentmem([
    "review", "list", "--root", evidenceRoot, "--status", "review_ready", "--limit", "20",
  ]);
  const packet = await runAgentmem([
    "review", "packet", "--root", evidenceRoot, "--status", "review_ready", "--limit", "20",
  ]);
  assert.equal(queue.candidates.length, 1);
  const item = queue.candidates[0];
  assert.equal(item.candidate.candidate_id, fixtureManifest.expected_candidate_id);
  assert.equal(item.candidate.text, fixtureManifest.expected_candidate_text);
  const basis = ["explicit_remember", "user_correction", "stable_repetition"].find(
    (candidate) => item.candidate.support_types.includes(candidate),
  );
  assert.ok(basis, "frozen candidate has no supported review basis");

  await runAgentmem([
    "review", "decide", "--root", evidenceRoot,
    "--candidate", item.candidate.candidate_id,
    "--action", "validate",
    "--reviewer", "synthetic-test-attestation",
    "--reviewer-kind", "synthetic_test",
    "--scope", "project",
    "--scope-value", "example-project",
    "--basis", basis,
    "--reason", "Recorded a simulated validation for the frozen synthetic fixture.",
  ]);
  const promoted = await runAgentmem([
    "promote", "candidate", "--root", evidenceRoot,
    "--candidate", item.candidate.candidate_id,
    "--approver", "synthetic-test-attestation",
    "--approver-kind", "synthetic_test",
    "--packet", packet.packet_id,
    "--reason", "Recorded a simulated promotion for the frozen synthetic fixture.",
  ]);
  await runAgentmem(["portable", "init", "--repo", portableRepo]);
  await runAgentmem(["portable", "export", "--root", evidenceRoot, "--repo", portableRepo]);
  const verified = await runAgentmem(["portable", "verify", "--repo", portableRepo]);
  assert.equal(verified.active_memories, 1);
  return promoted.revision.memory_id;
}

let server;
let passed = false;
let phase = "installed";
const requestBodies = [];
try {
  const promotedMemory = await seedVerifiedMemory();
  const install = await runDsh(["plugin", "--profile", "headless", "add", installSpec]);
  assert.equal(install.code, 0, install.stderr || install.stdout);

  const profileDir = path.join(home, "profiles", "headless");
  const installedRoot = path.join(profileDir, "node_modules", PACKAGE_NAME);
  await access(installedRoot);
  await writeFile(path.join(profileDir, "cordis.patch.yml"), profilePatch(), "utf8");

  const dump = await runDsh(["--profile", "headless", "--dump-config"]);
  assert.equal(dump.code, 0, dump.stderr || dump.stdout);
  const normalizedDump = `${dump.stdout}\n${dump.stderr}`.replace(/[\\/]+/g, "/");
  assert.ok(normalizedDump.includes(`# == ${PACKAGE_NAME}`));
  assert.ok(
    normalizedDump.includes(`name: ${PACKAGE_NAME}`) ||
      normalizedDump.includes(`name: '${PACKAGE_NAME}'`),
  );
  assert.ok(normalizedDump.includes("scopeProject: example-project"));
  assert.ok(normalizedDump.includes("captureDir:"));
  assert.ok(normalizedDump.includes("evidenceRoot:"));
  assert.ok(normalizedDump.includes("portableRepo:"));

  server = createServer(async (request, response) => {
    try {
      if (request.method !== "POST" || !request.url?.endsWith("/chat/completions")) {
        response.writeHead(404).end();
        return;
      }
      if (request.headers.authorization !== "Bearer deterministic-mock-key") {
        response.writeHead(401, { "content-type": "application/json" });
        response.end(JSON.stringify({ error: { message: "bad mock key" } }));
        return;
      }
      const body = await readBody(request);
      requestBodies.push(body);
      if (phase === "removed") finishText(response, AFTER_REMOVE_TEXT);
      else if (requestBodies.length === 1) finishText(response, FINAL_TEXT_ONE);
      else if (requestBodies.length === 2) finishText(response, FINAL_TEXT_TWO);
      else {
        response.writeHead(500, { "content-type": "application/json" });
        response.end(JSON.stringify({ error: { message: "mock script exhausted" } }));
      }
    } catch (error) {
      response.writeHead(500, { "content-type": "application/json" });
      response.end(JSON.stringify({ error: { message: String(error) } }));
    }
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  assert.ok(address && typeof address === "object");
  const modelEnv = {
    DEEPSEEK_API_KEY: "deterministic-mock-key",
    DEEPSEEK_BASE_URL: `http://127.0.0.1:${address.port}`,
    DSH_PERMISSION_MODE: "workspace-write",
    NO_PROXY: "127.0.0.1,localhost",
  };

  const first = await runDsh(["--profile", "headless", taskText], modelEnv);
  assert.equal(first.code, 0, first.stderr || first.stdout);
  assert.equal(first.stdout.trim(), FINAL_TEXT_ONE);
  assert.match(allStrings(requestBodies[0]).join("\n"), new RegExp(fixtureManifest.expected_candidate_text));

  const second = await runDsh(
    ["--profile", "headless", `Repeat using verified memory: ${taskText}`],
    modelEnv,
  );
  assert.equal(second.code, 0, second.stderr || second.stdout);
  assert.equal(second.stdout.trim(), FINAL_TEXT_TWO);
  assert.match(allStrings(requestBodies[1]).join("\n"), new RegExp(fixtureManifest.expected_candidate_text));

  const sessionLog = await readJsonlFiles(path.join(home, "sessions"));
  const recallEvents = sessionLog.records.filter(
    (record) => record.type === "user/message" && pluginRecall(record),
  );
  assert.equal(recallEvents.length, 2, "each accepted turn must persist one formal recall message");

  const capture = await readJsonlFiles(captureRoot);
  const capturedRecall = capture.records.filter(
    (record) => record.kind === "session_event" &&
      record.event?.type === "user/message" && pluginRecall(record.event),
  );
  assert.equal(capturedRecall.length, 2, "capture spool must preserve both formal recall events");
  assert.ok(capture.records.some((record) => record.kind === "raw_artifact"));

  const initialized = await run(agentmem, ["init", "--root", importEvidenceRoot]);
  assert.equal(initialized.code, 0, initialized.stderr || initialized.stdout);
  const imported = await runAgentmem([
    "import", "deepseek-harness-events", "--root", importEvidenceRoot, "--path", captureRoot,
  ]);
  assert.ok(imported.events_appended > 0);
  assert.equal(imported.gaps_appended, 0);
  assert.ok(imported.events_skipped > 0, "live/backfill duplicate events should be deduplicated");
  const importedAgain = await runAgentmem([
    "import", "deepseek-harness-events", "--root", importEvidenceRoot, "--path", captureRoot,
  ]);
  assert.equal(importedAgain.events_appended, 0);
  const doctor = await runAgentmem(["doctor", "--root", importEvidenceRoot]);
  assert.equal(doctor.ready, true);

  const recordsBeforeRemoval = capture.records.length;
  const remove = await runDsh(["plugin", "--profile", "headless", "remove", PACKAGE_NAME]);
  assert.equal(remove.code, 0, remove.stderr || remove.stdout);
  const afterRemove = await runDsh(["--profile", "headless", "--dump-config"]);
  assert.equal(afterRemove.code, 0, afterRemove.stderr || afterRemove.stdout);
  const normalizedAfterRemove = `${afterRemove.stdout}\n${afterRemove.stderr}`.replace(/[\\/]+/g, "/");
  assert.ok(!normalizedAfterRemove.includes(PACKAGE_NAME));
  await assert.rejects(access(installedRoot), { code: "ENOENT" });

  phase = "removed";
  const afterRemovalRun = await runDsh(
    ["--profile", "headless", "Run once without the Agent Memory Bundle."],
    modelEnv,
  );
  assert.equal(afterRemovalRun.code, 0, afterRemovalRun.stderr || afterRemovalRun.stdout);
  assert.equal(afterRemovalRun.stdout.trim(), AFTER_REMOVE_TEXT);
  assert.equal((await readJsonlFiles(captureRoot)).records.length, recordsBeforeRemoval);

  passed = true;
  console.log(JSON.stringify({
    status: "passed",
    promotedMemory,
    modelRequestsWithPlugin: 2,
    persistedRecallMessages: recallEvents.length,
    capturedRecallMessages: capturedRecall.length,
    rawArtifactBackfill: true,
    importedEvents: imported.events_appended,
    deduplicatedEvents: imported.events_skipped,
    importGaps: imported.gaps_appended,
    idempotentReimport: true,
    removed: true,
    sideEffectsAfterRemoval: false,
    artifactsKept: keepArtifacts,
    ...(keepArtifacts ? { artifactRoot: root } : {}),
  }));
} finally {
  if (server) await new Promise((resolve) => server.close(resolve));
  if (passed && !keepArtifacts) {
    if (
      path.dirname(root) !== path.resolve(scratchRoot) ||
      !path.basename(root).startsWith("dsh-agent-memory-")
    ) {
      throw new Error(`refusing to remove unexpected E2E directory: ${root}`);
    }
    await rm(root, { recursive: true, force: true });
  } else if (!passed) {
    console.error(`Deterministic E2E artifacts retained at ${root}`);
  }
}
