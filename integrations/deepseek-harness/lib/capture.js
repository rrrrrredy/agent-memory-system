import { createReadStream } from "node:fs";
import { readdir } from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";

import { CrashSafeJSONLSpool } from "./crash-safe-jsonl.js";

export const CAPTURE_SCHEMA = "deepseek-harness-capture/v1alpha1";

export class HarnessCapture {
  constructor(destination, options = {}) {
    this.destination = destination;
    this.logger = options.logger ?? { error() {}, warn() {} };
    this.spool = options.spool ?? new CrashSafeJSONLSpool(destination, {
      maxBytes: options.segmentMaxBytes,
      onRecoveryError: (file, error) => {
        this.recoveryErrors.push({ file: path.basename(file), error: errorMessage(error) });
      },
    });
    this.recoveryErrors = [];
    this.pending = new Set();
    this.fatalErrors = [];
    this.backfills = new Set();
  }

  async initialize() {
    this.backfills = await readBackfillKeys(this.destination);
    await this.spool.initialize();
    for (const recovery of this.recoveryErrors.splice(0)) {
      await this.append({
        schema_version: CAPTURE_SCHEMA,
        captured_at: new Date().toISOString(),
        kind: "gap",
        session: { id: "", header: null },
        revision: "",
        reason_code: "spool_tail_recovery_failed",
        reason: `${recovery.file}: ${recovery.error}`,
      });
    }
  }

  enqueue(record) {
    const task = this.append(record).catch((error) => {
      this.fatalErrors.push(error);
      this.logger.error(`dsh-agent-memory capture failed: ${errorMessage(error)}`);
    });
    this.pending.add(task);
    void task.finally(() => this.pending.delete(task));
    return task;
  }

  async append(record) {
    try {
      await this.spool.append(record);
      return;
    } catch (firstError) {
      try {
        await this.spool.append(record);
        return;
      } catch (secondError) {
        const gap = {
          schema_version: CAPTURE_SCHEMA,
          captured_at: new Date().toISOString(),
          kind: "gap",
          session: record.session ?? { id: "", header: null },
          revision: typeof record.revision === "string" ? record.revision : "",
          reason_code: "capture_append_failed",
          reason: `${errorMessage(firstError)}; retry: ${errorMessage(secondError)}`,
          failed_record: record,
        };
        try {
          await this.spool.append(gap);
        } catch (gapError) {
          throw new AggregateError(
            [firstError, secondError, gapError],
            "raw event and its explicit capture gap could not be persisted",
          );
        }
      }
    }
  }

  async captureEvent(session, event) {
    await this.append({
      schema_version: CAPTURE_SCHEMA,
      captured_at: new Date().toISOString(),
      kind: "session_event",
      session: sessionEnvelope(session),
      event,
    });
  }

  async backfill(sessionPersistence) {
    if (sessionPersistence?.supportsRawArtifacts !== true) {
      await this.append({
        schema_version: CAPTURE_SCHEMA,
        captured_at: new Date().toISOString(),
        kind: "gap",
        session: { id: "", header: null },
        revision: "",
        reason_code: "raw_artifacts_unsupported",
        reason: "configured session persistence backend does not expose verbatim session artifacts",
      });
      return;
    }
    let snapshots;
    try {
      snapshots = await sessionPersistence.listSnapshots();
    } catch (error) {
      await this.append(backfillGap("snapshot_listing_failed", error));
      return;
    }
    if (!Array.isArray(snapshots)) {
      await this.append(backfillGap("snapshot_listing_invalid", new Error("listSnapshots did not return an array")));
      return;
    }
    for (const snapshot of snapshots) {
      const sessionId = sessionID(snapshot?.header);
      const revision = String(snapshot?.revision ?? "");
      const key = backfillKey(sessionId, revision);
      if (this.backfills.has(key)) continue;
      try {
        const artifact = await sessionPersistence.readRaw(sessionId);
        if (!artifact || typeof artifact.content !== "string") {
          throw new Error("readRaw did not return a verbatim text artifact");
        }
        await this.append({
          schema_version: CAPTURE_SCHEMA,
          captured_at: new Date().toISOString(),
          kind: "raw_artifact",
          session: { id: sessionId, header: snapshot?.header ?? null },
          revision,
          artifact: {
            meta: artifact.meta ?? null,
            filename: typeof artifact.filename === "string" ? artifact.filename : "",
            content: artifact.content,
          },
        });
        this.backfills.add(key);
      } catch (error) {
        await this.append({
          ...backfillGap("raw_artifact_read_failed", error),
          session: { id: sessionId, header: snapshot?.header ?? null },
          revision,
        });
      }
    }
  }

  async drain() {
    await Promise.all([...this.pending]);
    await this.spool.drain();
    if (this.fatalErrors.length > 0) {
      throw new AggregateError(this.fatalErrors.splice(0), "one or more capture records were lost");
    }
  }
}

export function sessionEnvelope(session) {
  return { id: String(session?.id ?? ""), header: session?.header ?? null };
}

function sessionID(header) {
  return String(header?.id ?? header?.sessionId ?? "");
}

function backfillGap(reasonCode, error) {
  return {
    schema_version: CAPTURE_SCHEMA,
    captured_at: new Date().toISOString(),
    kind: "gap",
    session: { id: "", header: null },
    revision: "",
    reason_code: reasonCode,
    reason: errorMessage(error),
  };
}

function errorMessage(error) {
  const value = error instanceof Error ? error.message : String(error);
  return value.length <= 4096 ? value : `${value.slice(0, 4093)}...`;
}

function backfillKey(sessionId, revision) {
  return `${sessionId}\u0000${revision}`;
}

async function readBackfillKeys(destination) {
  const directory = path.dirname(destination);
  const extension = path.extname(destination);
  const stem = path.basename(destination, extension);
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") return new Set();
    throw error;
  }
  const files = entries
    .filter((entry) => entry.isFile() && !entry.name.endsWith(".partial.jsonl") &&
      (entry.name === path.basename(destination) ||
        (entry.name.startsWith(`${stem}.segment-`) && entry.name.endsWith(".jsonl"))))
    .map((entry) => path.join(directory, entry.name));
  const keys = new Set();
  for (const file of files) {
    const lines = readline.createInterface({ input: createReadStream(file), crlfDelay: Infinity });
    for await (const line of lines) {
      if (!line.trim()) continue;
      try {
        const record = JSON.parse(line);
        if (record?.schema_version !== CAPTURE_SCHEMA || record?.kind !== "raw_artifact") continue;
        keys.add(backfillKey(String(record.session?.id ?? ""), String(record.revision ?? "")));
      } catch {
        // Torn tails are recovered by CrashSafeJSONLSpool. They cannot prove a completed backfill.
      }
    }
  }
  return keys;
}
