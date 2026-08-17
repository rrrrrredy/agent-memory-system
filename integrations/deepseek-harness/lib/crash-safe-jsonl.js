import { createHash, randomUUID } from "node:crypto"
import { link, open, mkdir, readdir, realpath, stat, unlink } from "node:fs/promises"
import { basename, dirname, extname, join } from "node:path"

const DEFAULT_MAX_BYTES = 32 * 1024 * 1024
const COPY_BUFFER_BYTES = 64 * 1024

export class CrashSafeJSONLSpool {
  constructor(destination, options = {}) {
    if (typeof destination !== "string" || !destination.trim()) {
      throw new Error("JSONL spool destination is required")
    }
    if (!destination.toLowerCase().endsWith(".jsonl")) {
      throw new Error("JSONL spool destination must end with .jsonl")
    }
    const maxBytes = options.maxBytes ?? DEFAULT_MAX_BYTES
    if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) {
      throw new Error("JSONL spool maxBytes must be a positive safe integer")
    }
    const pid = options.pid ?? process.pid
    if (!Number.isSafeInteger(pid) || pid <= 0) {
      throw new Error("JSONL spool pid must be a positive safe integer")
    }
    const onRecoveryError = options.onRecoveryError ?? (() => {})
    if (typeof onRecoveryError !== "function") {
      throw new Error("JSONL spool onRecoveryError must be a function")
    }

    this.destination = destination
    this.maxBytes = maxBytes
    this.now = options.now ?? (() => new Date())
    this.unique = options.unique ?? randomUUID
    this.pid = pid
    this.onRecoveryError = onRecoveryError
    this.tail = Promise.resolve()
    this.initialized = false
    this.activePath = ""
    this.segmentSequence = 0
    this.pendingRecovery = new Set()
  }

  initialize() {
    return this.#enqueue(() => this.#ensureInitialized())
  }

  append(value) {
    return this.#enqueue(() => this.#appendNow(value))
  }

  drain() {
    return this.tail
  }

  #enqueue(operation) {
    const task = this.tail.then(operation)
    this.tail = task.catch(() => {})
    return task
  }

  async #ensureInitialized() {
    if (this.initialized) return
    await rejectGitPath(this.destination)
    await mkdir(dirname(this.destination), { recursive: true, mode: 0o700 })
    for (const path of this.pendingRecovery) {
      try {
        await recoverPartialTail(path)
      } catch (error) {
        this.#reportRecoveryError(path, error)
      } finally {
        this.pendingRecovery.delete(path)
      }
    }
    for (const path of await matchingSegments(this.destination)) {
      if (!isRecoverableSegment(this.destination, path)) continue
      try {
        await recoverPartialTail(path)
      } catch (error) {
        this.#reportRecoveryError(path, error)
      }
    }
    this.activePath = this.#newSegmentPath()
    this.initialized = true
  }

  async #appendNow(value) {
    const encoded = JSON.stringify(value)
    if (encoded === undefined) {
      throw new Error("spool value is not JSON serializable")
    }
    const line = Buffer.from(encoded + "\n", "utf8")
    await this.#ensureInitialized()
    const size = await regularFileSize(this.activePath)
    if (size !== null && size > 0 && size + line.length > this.maxBytes) {
      this.activePath = this.#newSegmentPath()
    }
    try {
      await durableAppend(this.activePath, line)
    } catch (error) {
      // A failed write may have left a partial tail. Reinitialize on the next
      // append so the tail is preserved before any new record is written.
      if (this.activePath) this.pendingRecovery.add(this.activePath)
      this.initialized = false
      this.activePath = ""
      throw error
    }
  }

  #newSegmentPath() {
    const extension = extname(this.destination)
    const stem = basename(this.destination, extension)
    const timestamp = safeTimestamp(this.now())
    const token = safeToken(this.unique())
    const sequence = this.segmentSequence++
    return join(dirname(this.destination),
      `${stem}.segment-p${this.pid}-${timestamp}-${sequence}-${token}.jsonl`)
  }

  #reportRecoveryError(path, error) {
    try {
      this.onRecoveryError(path, error)
    } catch {
      // Reporting must not prevent new evidence from being captured.
    }
  }
}

async function matchingSegments(destination) {
  const directory = dirname(destination)
  const extension = extname(destination)
  const base = basename(destination)
  const stem = basename(destination, extension)
  let entries
  try {
    entries = await readdir(directory, { withFileTypes: true })
  } catch (error) {
    if (error?.code === "ENOENT") return []
    throw error
  }
  return entries
    .filter((entry) => entry.isFile() &&
      (entry.name === base ||
        (entry.name.startsWith(`${stem}.segment-`) &&
          entry.name.endsWith(".jsonl") && !entry.name.endsWith(".partial.jsonl"))))
    .map((entry) => join(directory, entry.name))
    .sort()
}

function isRecoverableSegment(destination, path) {
  // The exact destination may belong to an older writer that has no ownership
  // marker. Preserve it as-is; current writers always use PID-tagged segments.
  if (path === destination) return false
  const match = basename(path).match(/\.segment-p(\d+)-/)
  if (!match) return true
  return !processIsAlive(Number.parseInt(match[1], 10))
}

function processIsAlive(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 0) return false
  try {
    process.kill(pid, 0)
    return true
  } catch (error) {
    if (error?.code === "ESRCH") return false
    return true
  }
}

async function recoverPartialTail(path) {
  let source
  try {
    source = await open(path, "r+")
  } catch (error) {
    if (error?.code === "ENOENT") return null
    throw error
  }
  try {
    const info = await source.stat()
    if (!info.isFile()) throw new Error(`spool segment is not a regular file: ${path}`)
    if (info.size === 0) return null

    const last = Buffer.alloc(1)
    await readFully(source, last, 0, 1, info.size - 1)
    if (last[0] === 0x0a) return null

    const fragmentStart = await findFragmentStart(source, info.size)
    const recovery = await preserveFragment(source, path, fragmentStart, info.size)
    await source.truncate(fragmentStart)
    await source.sync()
    return recovery
  } finally {
    await source.close()
  }
}

async function rejectGitPath(path) {
  let current = await resolveProjectedPath(dirname(path))
  while (true) {
    try {
      await stat(join(current, ".git"))
      throw new Error("raw JSONL spool must be outside a Git worktree")
    } catch (error) {
      if (error?.message === "raw JSONL spool must be outside a Git worktree") throw error
      if (error?.code !== "ENOENT") throw error
    }
    const parent = dirname(current)
    if (parent === current) return
    current = parent
  }
}

async function resolveProjectedPath(path) {
  let current = path
  const missing = []
  while (true) {
    try {
      let resolved = await realpath(current)
      for (let index = missing.length - 1; index >= 0; index--) {
        resolved = join(resolved, missing[index])
      }
      return resolved
    } catch (error) {
      if (error?.code !== "ENOENT") throw error
    }
    const parent = dirname(current)
    if (parent === current) return path
    missing.push(basename(current))
    current = parent
  }
}

async function findFragmentStart(source, size) {
  let position = size
  while (position > 0) {
    const start = Math.max(0, position - COPY_BUFFER_BYTES)
    const length = position - start
    const buffer = Buffer.allocUnsafe(length)
    await readFully(source, buffer, 0, length, start)
    const newline = buffer.lastIndexOf(0x0a)
    if (newline >= 0) return start + newline + 1
    position = start
  }
  return 0
}

async function preserveFragment(source, sourcePath, start, end) {
  const directory = dirname(sourcePath)
  const extension = extname(sourcePath)
  const stem = basename(sourcePath, extension)
  const temporary = join(directory,
    `.${stem}.recovery-${process.pid}-${safeToken(randomUUID())}.tmp`)
  const output = await open(temporary, "wx", 0o600)
  const hasher = createHash("sha256")
  let outputClosed = false
  try {
    const buffer = Buffer.allocUnsafe(COPY_BUFFER_BYTES)
    let sourcePosition = start
    let outputPosition = 0
    while (sourcePosition < end) {
      const length = Math.min(buffer.length, end - sourcePosition)
      await readFully(source, buffer, 0, length, sourcePosition)
      hasher.update(buffer.subarray(0, length))
      await writeFully(output, buffer, 0, length, outputPosition)
      sourcePosition += length
      outputPosition += length
    }
    await output.sync()
    await output.close()
    outputClosed = true

    const digest = hasher.digest("hex")
    const target = join(directory, `${stem}.recovered-${digest}.partial.jsonl`)
    try {
      await link(temporary, target)
    } catch (error) {
      if (error?.code !== "EEXIST") throw error
      const existingSize = await regularFileSize(target)
      if (existingSize !== end - start || await digestFile(target) !== digest) {
        throw new Error(`recovery artifact does not match its digest: ${target}`)
      }
    }
    await unlink(temporary)
    await syncDirectory(directory)
    return target
  } catch (error) {
    if (!outputClosed) await output.close().catch(() => {})
    await unlink(temporary).catch(() => {})
    throw error
  }
}

async function durableAppend(path, data) {
  const file = await open(path, "a", 0o600)
  try {
    await writeFully(file, data, 0, data.length, null)
    await file.sync()
  } finally {
    await file.close()
  }
}

async function readFully(file, buffer, offset, length, position) {
  let completed = 0
  while (completed < length) {
    const result = await file.read(buffer, offset + completed, length - completed,
      position + completed)
    if (result.bytesRead === 0) throw new Error("unexpected end of spool segment")
    completed += result.bytesRead
  }
}

async function writeFully(file, buffer, offset, length, position) {
  let completed = 0
  while (completed < length) {
    const writePosition = position === null ? null : position + completed
    const result = await file.write(buffer, offset + completed, length - completed,
      writePosition)
    if (result.bytesWritten === 0) throw new Error("zero-byte spool write")
    completed += result.bytesWritten
  }
}

async function regularFileSize(path) {
  try {
    const info = await stat(path)
    if (!info.isFile()) throw new Error(`spool path is not a regular file: ${path}`)
    return info.size
  } catch (error) {
    if (error?.code === "ENOENT") return null
    throw error
  }
}

async function digestFile(path) {
  const file = await open(path, "r")
  const hasher = createHash("sha256")
  try {
    const buffer = Buffer.allocUnsafe(COPY_BUFFER_BYTES)
    let position = 0
    while (true) {
      const result = await file.read(buffer, 0, buffer.length, position)
      if (result.bytesRead === 0) break
      hasher.update(buffer.subarray(0, result.bytesRead))
      position += result.bytesRead
    }
  } finally {
    await file.close()
  }
  return hasher.digest("hex")
}

async function syncDirectory(path) {
  let directory
  try {
    directory = await open(path, "r")
    await directory.sync()
  } catch (error) {
    if (!["EISDIR", "EINVAL", "ENOTSUP", "EPERM", "EACCES"].includes(error?.code)) {
      throw error
    }
  } finally {
    await directory?.close().catch(() => {})
  }
}

function safeTimestamp(value) {
  const date = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(date.getTime())) throw new Error("spool timestamp is invalid")
  return date.toISOString().replaceAll(":", "-").replaceAll(".", "-")
}

function safeToken(value) {
  const token = String(value).replaceAll(/[^a-zA-Z0-9_-]/g, "-")
  if (!token) throw new Error("spool unique token is empty")
  return token
}
