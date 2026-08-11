import { spawn } from "node:child_process"

const DEFAULT_LIMIT = 5
const DEFAULT_TOKEN_BUDGET = 600
const DEFAULT_BYTE_BUDGET = 3072
const DEFAULT_TIMEOUT_MS = 3000
const DEFAULT_MAX_OUTPUT_BYTES = 128 * 1024
const DEFAULT_MAX_SESSIONS = 128

export function retrievalConfigFromEnv(env = process.env) {
  const evidenceRoot = value(env.AGENT_MEMORY_EVIDENCE_ROOT)
  const portableRoot = value(env.AGENT_MEMORY_PORTABLE_REPO)
  if (!evidenceRoot || !portableRoot) return null

  return {
    binary: value(env.AGENT_MEMORY_BINARY) || "agentmem",
    evidenceRoot,
    portableRoot,
    repository: value(env.AGENT_MEMORY_SCOPE_REPOSITORY),
    project: value(env.AGENT_MEMORY_SCOPE_PROJECT),
    task: value(env.AGENT_MEMORY_SCOPE_TASK),
    limit: positiveInteger(env.AGENT_MEMORY_RETRIEVAL_LIMIT, DEFAULT_LIMIT),
    tokenBudget: positiveInteger(
      env.AGENT_MEMORY_RETRIEVAL_TOKEN_BUDGET,
      DEFAULT_TOKEN_BUDGET,
    ),
    byteBudget: positiveInteger(
      env.AGENT_MEMORY_RETRIEVAL_BYTE_BUDGET,
      DEFAULT_BYTE_BUDGET,
    ),
    timeoutMs: positiveInteger(
      env.AGENT_MEMORY_RETRIEVAL_TIMEOUT_MS,
      DEFAULT_TIMEOUT_MS,
    ),
    maxOutputBytes: DEFAULT_MAX_OUTPUT_BYTES,
  }
}

export function extractPrompt(parts) {
  if (!Array.isArray(parts)) return ""
  return parts
    .filter((part) => part && part.type === "text" && part.synthetic !== true)
    .map((part) => typeof part.text === "string" ? part.text.trim() : "")
    .filter(Boolean)
    .join("\n\n")
}

export class OpenCodeMemoryBridge {
  constructor(config, options = {}) {
    if (!config || !value(config.evidenceRoot) || !value(config.portableRoot)) {
      throw new Error("OpenCode retrieval requires evidence and portable memory roots")
    }
    this.config = config
    this.run = options.run ?? runAgentMemory
    this.onError = options.onError ?? (() => {})
  }

  async retrieve(request) {
    const sessionID = value(request?.sessionID)
    const prompt = value(request?.prompt)
    if (!sessionID || !prompt) return ""

    const eventName = request.eventName === "SessionCompacting"
      ? "SessionCompacting"
      : "UserPromptSubmit"
    const input = {
      session_id: sessionID,
      cwd: value(request.cwd),
      hook_event_name: eventName,
      turn_id: value(request.messageID),
      prompt,
      source: value(request.source) || "opencode-plugin",
    }

    try {
      const raw = await this.run({
        binary: this.config.binary || "agentmem",
        args: buildArguments(this.config),
        input,
        timeoutMs: this.config.timeoutMs || DEFAULT_TIMEOUT_MS,
        maxOutputBytes: this.config.maxOutputBytes || DEFAULT_MAX_OUTPUT_BYTES,
      })
      const output = typeof raw === "string" ? JSON.parse(raw) : raw
      if (!output || output.continue !== true) {
        throw new Error("agentmem returned an invalid continuation response")
      }
      const context = output.hookSpecificOutput?.additionalContext
      return typeof context === "string" ? context : ""
    } catch (error) {
      try {
        await this.onError(error)
      } catch {
        // Error reporting must not turn optional retrieval into a task failure.
      }
      return ""
    }
  }
}

export function createOpenCodeRetrievalHooks({
  bridge,
  directory = "",
  maxSessions = DEFAULT_MAX_SESSIONS,
}) {
  if (!bridge || typeof bridge.retrieve !== "function") {
    throw new Error("OpenCode retrieval bridge is required")
  }
  if (!Number.isSafeInteger(maxSessions) || maxSessions <= 0) {
    throw new Error("OpenCode retrieval session limit must be positive")
  }

  const sessions = new Map()
  const remember = (sessionID, state) => {
    sessions.delete(sessionID)
    sessions.set(sessionID, state)
    while (sessions.size > maxSessions) {
      sessions.delete(sessions.keys().next().value)
    }
  }

  return {
    "chat.message": async (input, output) => {
      const sessionID = value(input?.sessionID)
      if (!sessionID) return
      const prompt = extractPrompt(output?.parts)
      if (!prompt) return

      const messageID = value(input?.messageID)
      remember(sessionID, { messageID, prompt })
    },
    "experimental.chat.system.transform": async (input, output) => {
      const sessionID = value(input?.sessionID)
      const current = sessions.get(sessionID)
      if (!current || !Array.isArray(output?.system)) return

      const context = await bridge.retrieve({
        sessionID,
        messageID: current.messageID,
        prompt: current.prompt,
        cwd: directory,
        eventName: "UserPromptSubmit",
        source: "experimental.chat.system.transform",
      })
      if (context) output.system.push(context)
    },
    "experimental.session.compacting": async (input, output) => {
      const sessionID = value(input?.sessionID)
      const current = sessions.get(sessionID)
      if (!current || !Array.isArray(output?.context)) return

      const context = await bridge.retrieve({
        sessionID,
        messageID: current.messageID,
        prompt: current.prompt,
        cwd: directory,
        eventName: "SessionCompacting",
        source: "experimental.session.compacting",
      })
      if (context) output.context.push(context)
    },
  }
}

export function buildArguments(config) {
  const args = [
    "inject",
    "opencode",
    "--root",
    config.evidenceRoot,
    "--repo",
    config.portableRoot,
    "--limit",
    String(config.limit || DEFAULT_LIMIT),
    "--token-budget",
    String(config.tokenBudget || DEFAULT_TOKEN_BUDGET),
    "--byte-budget",
    String(config.byteBudget || DEFAULT_BYTE_BUDGET),
  ]
  appendScope(args, "--scope-repository", config.repository)
  appendScope(args, "--scope-project", config.project)
  appendScope(args, "--scope-task", config.task)
  return args
}

export function runAgentMemory({
  binary,
  args,
  input,
  timeoutMs = DEFAULT_TIMEOUT_MS,
  maxOutputBytes = DEFAULT_MAX_OUTPUT_BYTES,
}) {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, {
      shell: false,
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    })
    let settled = false
    let stdoutBytes = 0
    let stderrBytes = 0
    const stdout = []
    const stderr = []
    const finish = (error, result) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      if (error) reject(error)
      else resolve(result)
    }
    const timer = setTimeout(() => {
      child.kill()
      finish(new Error("agentmem retrieval timed out"))
    }, timeoutMs)

    child.once("error", (error) => finish(error))
    child.stdout.on("data", (chunk) => {
      stdoutBytes += chunk.length
      if (stdoutBytes > maxOutputBytes) {
        child.kill()
        finish(new Error("agentmem retrieval output exceeded its limit"))
        return
      }
      stdout.push(chunk)
    })
    child.stderr.on("data", (chunk) => {
      stderrBytes += chunk.length
      if (stderrBytes <= maxOutputBytes) stderr.push(chunk)
    })
    child.once("close", (code) => {
      if (code !== 0) {
        const detail = Buffer.concat(stderr).toString("utf8").trim()
        finish(new Error(detail || `agentmem retrieval exited with code ${code}`))
        return
      }
      finish(null, Buffer.concat(stdout).toString("utf8"))
    })
    child.stdin.once("error", (error) => finish(error))
    child.stdin.end(JSON.stringify(input))
  })
}

function appendScope(args, flag, scope) {
  const normalized = value(scope)
  if (normalized) args.push(flag, normalized)
}

function positiveInteger(raw, fallback) {
  const normalized = value(raw)
  if (!/^[1-9]\d*$/.test(normalized)) return fallback
  const parsed = Number(normalized)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : fallback
}

function value(raw) {
  return typeof raw === "string" ? raw.trim() : ""
}
