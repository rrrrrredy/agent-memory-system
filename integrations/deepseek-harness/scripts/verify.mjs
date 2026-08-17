import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const integrationRoot = fileURLToPath(new URL("../", import.meta.url));
const tests = [
  "tests/capture.test.mjs",
  "tests/config.test.mjs",
  "tests/plugin.test.mjs",
  "tests/process.test.mjs",
  "tests/retrieval.test.mjs",
];

await import("./build.mjs");
await run(process.execPath, ["--test", ...tests]);

function run(executable, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      cwd: integrationRoot,
      shell: false,
      stdio: "inherit",
      windowsHide: true,
    });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0 && signal === null) {
        resolve();
        return;
      }
      reject(new Error(`Bundle verification exited (code ${String(code)}, signal ${String(signal)})`));
    });
  });
}
