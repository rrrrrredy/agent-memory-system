export interface CrashSafeJSONLSpoolOptions {
  maxBytes?: number
  now?: () => Date
  unique?: () => string
  pid?: number
  onRecoveryError?: (path: string, error: unknown) => void
}

export declare class CrashSafeJSONLSpool {
  constructor(destination: string, options?: CrashSafeJSONLSpoolOptions)
  initialize(): Promise<void>
  append(value: unknown): Promise<void>
  drain(): Promise<void>
}
