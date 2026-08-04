import type { Plugin } from "@opencode-ai/plugin"
import {
  createOpenCodeRetrievalHooks,
  OpenCodeMemoryBridge,
  retrievalConfigFromEnv,
} from "./opencode-retrieval.mjs"

export const AgentMemoryRetrievalPlugin: Plugin = async ({ client, directory }) => {
  const config = retrievalConfigFromEnv(process.env)
  if (!config) return {}

  const bridge = new OpenCodeMemoryBridge(config, {
    onError: async (error: unknown) => {
      try {
        await client.app.log({
          body: {
            service: "agent-memory",
            level: "warn",
            message: "Memory retrieval was skipped",
            extra: { error: error instanceof Error ? error.message : String(error) },
          },
        })
      } catch {
        // Retrieval remains fail-open even when structured logging is unavailable.
      }
    },
  })
  return createOpenCodeRetrievalHooks({ bridge, directory })
}
