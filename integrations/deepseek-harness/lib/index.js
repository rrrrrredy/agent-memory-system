import path from "node:path";

import z from "@deepseek-ai/schemastery";

import { HarnessCapture, CAPTURE_SCHEMA, sessionEnvelope } from "./capture.js";
import { extractUserPrompt, memoryMessage, retrieveMemory } from "./retrieval.js";

const DEFAULTS = Object.freeze({
  binary: "agentmem",
  captureDir: "",
  evidenceRoot: "",
  portableRepo: "",
  scopeRepository: "",
  scopeProject: "",
  scopeTask: "",
  limit: 5,
  tokenBudget: 600,
  byteBudget: 3072,
  timeoutMs: 3000,
  stdoutMaxBytes: 128 * 1024,
  stderrMaxBytes: 128 * 1024,
  segmentMaxBytes: 32 * 1024 * 1024,
});
const MAX_BYTES = 64 * 1024 * 1024;

export const name = "agent-memory";
export const inject = ["sessionPersistence", "subprocess"];
export { CAPTURE_SCHEMA };
export const Config = z.object({
  binary: z.string().default(DEFAULTS.binary),
  captureDir: z.string().default(DEFAULTS.captureDir),
  evidenceRoot: z.string().default(DEFAULTS.evidenceRoot),
  portableRepo: z.string().default(DEFAULTS.portableRepo),
  scopeRepository: z.string().default(DEFAULTS.scopeRepository),
  scopeProject: z.string().default(DEFAULTS.scopeProject),
  scopeTask: z.string().default(DEFAULTS.scopeTask),
  limit: z.number().step(1).min(1).default(DEFAULTS.limit),
  tokenBudget: z.number().step(1).min(1).default(DEFAULTS.tokenBudget),
  byteBudget: z.number().step(1).min(1).max(MAX_BYTES).default(DEFAULTS.byteBudget),
  timeoutMs: z.number().step(1).min(1).default(DEFAULTS.timeoutMs),
  stdoutMaxBytes: z.number().step(1).min(1).max(MAX_BYTES).default(DEFAULTS.stdoutMaxBytes),
  stderrMaxBytes: z.number().step(1).min(1).max(MAX_BYTES).default(DEFAULTS.stderrMaxBytes),
  segmentMaxBytes: z.number().step(1).min(1).max(MAX_BYTES).default(DEFAULTS.segmentMaxBytes),
});

export function normalizeConfig(supplied = {}) {
  const config = { ...DEFAULTS, ...supplied };
  for (const key of [
    "binary", "captureDir", "evidenceRoot", "portableRepo",
    "scopeRepository", "scopeProject", "scopeTask",
  ]) {
    if (typeof config[key] !== "string") throw new Error(`dsh-agent-memory: \`${key}\` must be a string`);
    config[key] = config[key].trim();
  }
  if (!config.binary) throw new Error("dsh-agent-memory: `binary` must not be empty");
  for (const key of [
    "limit", "tokenBudget", "byteBudget", "timeoutMs",
    "stdoutMaxBytes", "stderrMaxBytes", "segmentMaxBytes",
  ]) {
    if (!Number.isSafeInteger(config[key]) || config[key] < 1) {
      throw new Error(`dsh-agent-memory: \`${key}\` must be a positive safe integer`);
    }
  }
  for (const key of ["byteBudget", "stdoutMaxBytes", "stderrMaxBytes", "segmentMaxBytes"]) {
    if (config[key] > MAX_BYTES) {
      throw new Error(`dsh-agent-memory: \`${key}\` must not exceed ${MAX_BYTES}`);
    }
  }
  if (Boolean(config.evidenceRoot) !== Boolean(config.portableRepo)) {
    throw new Error("dsh-agent-memory: `evidenceRoot` and `portableRepo` must be configured together");
  }
  for (const key of ["captureDir", "evidenceRoot", "portableRepo"]) {
    if (config[key] && !path.isAbsolute(config[key])) {
      throw new Error(`dsh-agent-memory: \`${key}\` must be an absolute path`);
    }
  }
  return Object.freeze(config);
}

export async function apply(ctx, suppliedConfig) {
  const config = normalizeConfig(suppliedConfig);
  const captureEnabled = Boolean(config.captureDir);
  const retrievalEnabled = Boolean(config.evidenceRoot && config.portableRepo);
  if (!captureEnabled && !retrievalEnabled) {
    ctx.logger.info("dsh-agent-memory loaded inertly; configure captureDir and/or retrieval roots to opt in");
    return;
  }

  let capture;
  if (captureEnabled) {
    const destination = path.join(config.captureDir, "deepseek-harness-events.jsonl");
    capture = new HarnessCapture(destination, {
      logger: ctx.logger,
      segmentMaxBytes: config.segmentMaxBytes,
    });
    await capture.initialize();
    await capture.backfill(ctx.sessionPersistence);

    ctx.on("session/event", (session, event) => {
      capture.enqueue({
        schema_version: CAPTURE_SCHEMA,
        captured_at: new Date().toISOString(),
        kind: "session_event",
        session: sessionEnvelope(session),
        event,
      });
    });
    ctx.on("session/flush", async () => {
      await capture.drain();
    });
  }

  const attemptedTurns = new WeakMap();
  if (retrievalEnabled) {
    ctx.on("agent/pre-step", async ({ agent, messages, turn, step, signal }, next) => {
      const decision = await next();
      if (decision.kind !== "enter" || signal.aborted) return decision;
      const prompt = extractUserPrompt(messages);
      if (!prompt) return decision;

      let attempts = attemptedTurns.get(agent.session);
      if (!attempts) {
        attempts = new Set();
        attemptedTurns.set(agent.session, attempts);
      }
      if (attempts.has(turn)) return decision;
      attempts.add(turn);

      try {
        const context = await retrieveMemory(ctx, config, {
          sessionId: String(agent.session?.id ?? ""),
          cwd: agent.session?.header?.cwd || process.cwd(),
          turn,
          step,
          prompt,
          signal,
        });
        if (!context) return decision;
        ctx.logger.info(`dsh-agent-memory injected verified memory for session ${String(agent.session?.id ?? "")} turn ${String(turn)}`);
        return { kind: "enter", messages: [...decision.messages, memoryMessage(context)] };
      } catch (error) {
        if (signal.aborted) return decision;
        ctx.logger.warn(`dsh-agent-memory retrieval skipped: ${formatError(error)}`);
        return decision;
      }
    });
  }

  ctx.on("session/disposed", (session) => attemptedTurns.delete(session));
  ctx.effect(
    () => async () => {
      if (capture) await capture.drain();
    },
    "dsh-agent-memory.flush-on-dispose",
  );
  ctx.logger.info(
    `dsh-agent-memory activated (capture=${String(captureEnabled)}, retrieval=${String(retrievalEnabled)})`,
  );
}

function formatError(error) {
  const value = error instanceof Error ? error.message : String(error);
  return value.length <= 1024 ? value : `${value.slice(0, 1021)}...`;
}
