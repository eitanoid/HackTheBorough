import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "vega-lite";

const dir = fileURLToPath(new URL("../../backend/internal/charts/testdata", import.meta.url));
const files = (await readdir(dir)).filter((file) => file.endsWith(".golden.json")).sort();

if (files.length === 0) {
  console.error(`no golden chart specs in ${dir}`);
  process.exit(1);
}

let failed = 0;
for (const file of files) {
  try {
    compile(JSON.parse(await readFile(path.join(dir, file), "utf8")));
    console.log(`ok ${file}`);
  } catch (error) {
    failed++;
    console.error(`FAIL ${file}: ${error instanceof Error ? error.message : error}`);
  }
}

if (failed > 0) process.exit(1);
