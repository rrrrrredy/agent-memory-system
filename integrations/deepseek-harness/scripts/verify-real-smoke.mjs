import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = path.resolve(fileURLToPath(new URL("../../../", import.meta.url)));

function required(name) {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function requiredPath(name) {
  const value = required(name);
  if (!path.isAbsolute(value)) throw new Error(`${name} must be an absolute path`);
  return path.resolve(value);
}

function isWithin(candidate, parent) {
  const relative = path.relative(parent, candidate);
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

function allStrings(value, output = []) {
  if (typeof value === "string") output.push(value);
  else if (Array.isArray(value)) for (const item of value) allStrings(item, output);
  else if (value && typeof value === "object") {
    for (const item of Object.values(value)) allStrings(item, output);
  }
  return output;
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

async function filesUnder(root) {
  const files = [];
  async function walk(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const candidate = path.join(directory, entry.name);
      if (entry.isDirectory()) await walk(candidate);
      else if (entry.isFile()) files.push(candidate);
    }
  }
  await walk(root);
  return files;
}

async function jsonlRecords(root) {
  const records = [];
  const files = (await filesUnder(root)).filter(
    (file) => file.endsWith(".jsonl") && !file.endsWith(".partial.jsonl"),
  );
  for (const file of files) {
    if (isWithin(file, repositoryRoot)) {
      throw new Error(`raw smoke artifact unexpectedly resides inside the repository: ${file}`);
    }
    const lines = (await readFile(file, "utf8")).split(/\r?\n/).filter(Boolean);
    for (const [index, line] of lines.entries()) {
      try {
        records.push({ file, record: JSON.parse(line) });
      } catch (error) {
        throw new Error(`malformed JSONL at ${file}:${index + 1}`, { cause: error });
      }
    }
  }
  return { files, records };
}

const smokeHome = requiredPath("DSH_SMOKE_HOME");
const captureRoot = requiredPath("DSH_SMOKE_CAPTURE");
const marker = required("DSH_SMOKE_MARKER");
const expectedMemory = required("DSH_SMOKE_EXPECTED_MEMORY");
const expectedMemoryID = required("DSH_SMOKE_EXPECTED_MEMORY_ID");
const finalToken = required("DSH_SMOKE_FINAL_TOKEN");

for (const candidate of [smokeHome, captureRoot]) {
  if (isWithin(candidate, repositoryRoot)) {
    throw new Error("real-smoke raw artifacts must stay outside the repository");
  }
}

const sessions = await jsonlRecords(path.join(smokeHome, "sessions"));
const sessionIDs = new Set();
for (const { file, record } of sessions.records) {
  if (
    record.type === "user/message" &&
    record.data?.source?.kind === "user" &&
    allStrings(record.data).some((value) => value.includes(marker))
  ) {
    sessionIDs.add(file);
  }
}
assert.equal(sessionIDs.size, 1, `expected one session containing the marker, found ${sessionIDs.size}`);
const [sessionFile] = sessionIDs;
const targetRecords = sessions.records
  .filter(({ file }) => file === sessionFile)
  .map(({ record }) => record);
const userSeq = Math.max(...targetRecords
  .filter(
    (record) => record.type === "user/message" && record.data?.source?.kind === "user" &&
      allStrings(record.data).some((value) => value.includes(marker)),
  )
  .map((record) => Number(record.seq)));
const recallSeq = Math.max(...targetRecords
  .filter(
    (record) => record.type === "user/message" && pluginRecall(record) &&
      allStrings(record.data).some(
        (value) => value.includes(expectedMemory) && value.includes(expectedMemoryID),
      ),
  )
  .map((record) => Number(record.seq)));
const finalSeq = Math.max(...targetRecords
  .filter(
    (record) => record.type === "assistant/message" &&
      allStrings(record.data).some((value) => value.includes(finalToken)),
  )
  .map((record) => Number(record.seq)));
assert.ok(userSeq >= 0, "real smoke user message was not persisted");
assert.ok(recallSeq > userSeq, "verified Agent Memory recall was not persisted after the user message");
assert.ok(finalSeq > recallSeq, "final model response did not follow the persisted recall message");

const capture = await jsonlRecords(captureRoot);
const capturedRecall = capture.records.filter(
  ({ record }) =>
    record.kind === "session_event" &&
    record.event?.type === "user/message" &&
    pluginRecall(record.event) &&
    allStrings(record.event).some(
      (value) => value.includes(expectedMemory) && value.includes(expectedMemoryID),
    ),
);
assert.equal(capturedRecall.length, 1, "capture spool must contain exactly one real-smoke recall event");

console.log(JSON.stringify({
  status: "passed",
  sessionFiles: sessions.files.length,
  markerSessionCount: sessionIDs.size,
  formalRecallMessages: 1,
  capturedRecallMessages: capturedRecall.length,
  finalResponseAfterRecall: true,
}));
