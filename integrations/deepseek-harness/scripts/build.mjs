import { createHash } from "node:crypto";
import { copyFile, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const integrationRoot = fileURLToPath(new URL("../", import.meta.url));
const repositoryRoot = fileURLToPath(new URL("../../../", import.meta.url));
const sourceRoot = path.join(integrationRoot, "src");
const outputRoot = path.join(integrationRoot, "lib");
const canonicalSpool = path.join(repositoryRoot, "integrations", "opencode", "crash-safe-jsonl.mjs");
const bundledSpool = path.join(outputRoot, "crash-safe-jsonl.js");
const manifestPath = path.join(integrationRoot, "source-integrity.json");

await Promise.all([
  rm(outputRoot, { recursive: true, force: true }),
  rm(path.join(integrationRoot, "LICENSE"), { force: true }),
  rm(manifestPath, { force: true }),
]);
await mkdir(outputRoot, { recursive: true });

const entries = await readdir(sourceRoot, { withFileTypes: true });
for (const entry of entries) {
  if (!entry.isFile() || !entry.name.endsWith(".js")) continue;
  await copyFile(path.join(sourceRoot, entry.name), path.join(outputRoot, entry.name));
}
await copyFile(canonicalSpool, bundledSpool);
await copyFile(path.join(repositoryRoot, "LICENSE"), path.join(integrationRoot, "LICENSE"));

const sourceDigest = digest(await readFile(canonicalSpool));
const bundledDigest = digest(await readFile(bundledSpool));
if (sourceDigest !== bundledDigest) {
  throw new Error("bundled crash-safe JSONL implementation differs from its canonical source");
}
await writeFile(
  manifestPath,
  `${JSON.stringify({
    schema_version: "dsh-agent-memory-source-integrity/v1",
    files: [{
      source: "integrations/opencode/crash-safe-jsonl.mjs",
      bundled: "lib/crash-safe-jsonl.js",
      sha256: sourceDigest,
    }],
  }, null, 2)}\n`,
  "utf8",
);

console.log(`Built ${entries.filter((entry) => entry.isFile() && entry.name.endsWith(".js")).length + 1} runtime modules.`);

function digest(data) {
  return createHash("sha256").update(data).digest("hex");
}
