import type { Plugin } from "@opencode-ai/plugin"
import { CrashSafeJSONLSpool } from "./crash-safe-jsonl.mjs"

const destination = process.env.AGENT_MEMORY_OPENCODE_EVENT_LOG
const configuredMaxBytes = Number.parseInt(
  process.env.AGENT_MEMORY_OPENCODE_EVENT_MAX_BYTES ?? "",
  10,
)

export const AgentMemoryEvidencePlugin: Plugin = async () => {
  if (!destination) return {}

  const spool = new CrashSafeJSONLSpool(destination, {
    maxBytes: Number.isSafeInteger(configuredMaxBytes) && configuredMaxBytes > 0
      ? configuredMaxBytes
      : undefined,
    onRecoveryError: (path: string, error: unknown) => {
      console.error("[agent-memory] spool recovery failed", path, error)
    },
  })
  const preserve = async (event: unknown) => {
    try {
      await spool.append({
        captured_at: new Date().toISOString(),
        event,
      })
    } catch (error) {
      console.error("[agent-memory] event capture failed", error)
    }
  }

  return {
    event: async ({ event }) => {
      await preserve(event)
    },
  }
}
