function abortScope(externalSignal, timeoutMs, label) {
  const controller = new AbortController();
  let timedOut = false;
  const timeout = setTimeout(() => {
    timedOut = true;
    controller.abort(new Error(`${label} exceeded ${timeoutMs}ms`));
  }, timeoutMs);
  const onAbort = () => controller.abort(externalSignal.reason);
  if (externalSignal?.aborted) onAbort();
  else externalSignal?.addEventListener("abort", onAbort, { once: true });
  return {
    signal: controller.signal,
    timedOut: () => timedOut,
    dispose() {
      clearTimeout(timeout);
      externalSignal?.removeEventListener("abort", onAbort);
    },
  };
}

function readCollected(handle, stream) {
  const reader = handle.collected[stream];
  if (!reader) throw new Error(`Agent Memory ${stream} was not collected`);
  return reader.readFrom(0);
}

export async function runAgentMemory(ctx, options) {
  const {
    binary,
    args,
    cwd,
    input,
    timeoutMs,
    stdoutMaxBytes,
    stderrMaxBytes,
    signal,
  } = options;
  const label = "Agent Memory retrieval";
  const scope = abortScope(signal, timeoutMs, label);
  let executable;
  let handle;
  let outcome;
  try {
    executable = await ctx.subprocess.resolveExecutable(binary, cwd, scope.signal);
    handle = ctx.subprocess.spawn({
      argv: [executable, ...args],
      cwd,
      stdio: {
        stdin: { data: `${JSON.stringify(input)}\n` },
        stdout: { maxBytes: stdoutMaxBytes },
        stderr: { maxBytes: stderrMaxBytes },
      },
      graceMs: Math.max(1, Math.min(1000, timeoutMs)),
      signal: scope.signal,
    });
    outcome = await handle.done;
  } catch (error) {
    signal?.throwIfAborted();
    if (scope.timedOut()) {
      throw new Error(`${label} timed out after ${timeoutMs}ms`, { cause: error });
    }
    throw new Error(`${label} could not run`, { cause: error });
  } finally {
    scope.dispose();
  }

  signal?.throwIfAborted();
  if (scope.timedOut()) throw new Error(`${label} timed out after ${timeoutMs}ms`);
  const stdout = readCollected(handle, "stdout");
  const stderr = readCollected(handle, "stderr");
  if (stdout.lossy) throw new Error(`${label} stdout exceeded ${stdoutMaxBytes} bytes`);
  if (stderr.lossy) throw new Error(`${label} stderr exceeded ${stderrMaxBytes} bytes`);
  if (outcome.exitCode !== 0 || outcome.signal !== null) {
    const detail = stderr.text.trim();
    throw new Error(
      `${label} exited unexpectedly (code ${String(outcome.exitCode)}, signal ${String(outcome.signal)})${detail ? `: ${detail}` : ""}`,
    );
  }
  return stdout.text;
}
