import type { Plugin } from "@opencode-ai/plugin"
import { appendFile, mkdir } from "node:fs/promises"
import { dirname } from "node:path"

const destination = process.env.AGENT_MEMORY_OPENCODE_EVENT_LOG

export const AgentMemoryEvidencePlugin: Plugin = async () => {
  if (!destination) return {}

  let tail = Promise.resolve()
  const preserve = async (event: unknown) => {
    let line: string
    try {
      line = JSON.stringify({
        captured_at: new Date().toISOString(),
        event,
      })
    } catch (error) {
      console.error("[agent-memory] event serialization failed", error)
      return
    }
    tail = tail
      .then(async () => {
        await mkdir(dirname(destination), { recursive: true })
        await appendFile(destination, line + "\n", { encoding: "utf8" })
      })
      .catch((error) => {
        console.error("[agent-memory] event capture failed", error)
      })
    await tail
  }

  return {
    event: async ({ event }) => {
      await preserve(event)
    },
  }
}
