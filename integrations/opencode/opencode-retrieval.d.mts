export interface OpenCodeRetrievalConfig {
  binary: string
  evidenceRoot: string
  portableRoot: string
  repository: string
  project: string
  task: string
  limit: number
  tokenBudget: number
  byteBudget: number
  timeoutMs: number
  maxOutputBytes: number
}

export declare function retrievalConfigFromEnv(
  env?: Record<string, string | undefined>,
): OpenCodeRetrievalConfig | null

export declare class OpenCodeMemoryBridge {
  constructor(config: OpenCodeRetrievalConfig, options?: {
    run?: (request: unknown) => Promise<string | unknown>
    onError?: (error: unknown) => void | Promise<void>
  })
  retrieve(request: {
    sessionID: string
    messageID?: string
    prompt: string
    cwd?: string
    eventName?: "UserPromptSubmit" | "SessionCompacting"
    source?: string
  }): Promise<string>
}

export interface OpenCodeRetrievalHooks {
  "chat.message": (input: any, output: any) => Promise<void>
  "experimental.chat.system.transform": (input: any, output: any) => Promise<void>
  "experimental.session.compacting": (input: any, output: any) => Promise<void>
}

export declare function createOpenCodeRetrievalHooks(input: {
  bridge: OpenCodeMemoryBridge
  directory?: string
  maxSessions?: number
}): OpenCodeRetrievalHooks

export declare function extractPrompt(parts: unknown): string
export declare function buildArguments(config: OpenCodeRetrievalConfig): string[]
export declare function runAgentMemory(request: unknown): Promise<string>
