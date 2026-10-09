// Builds the UI into dist/, which Go embeds (frontend/embed.go).
// Usage: bun run build.ts [--watch]
import { readdirSync, rmSync, watch } from "node:fs";
import { join } from "node:path";

const root = import.meta.dir;
const outdir = join(root, "dist");

async function build() {
  for (const f of readdirSync(outdir)) {
    if (f !== ".gitkeep") rmSync(join(outdir, f), { recursive: true, force: true });
  }
  const started = performance.now();
  const result = await Bun.build({
    entrypoints: [join(root, "index.html")],
    outdir,
    minify: !process.argv.includes("--watch"),
    sourcemap: process.argv.includes("--watch") ? "linked" : "none",
  });
  if (!result.success) {
    for (const log of result.logs) console.error(log);
    return false;
  }
  console.log(`built ${result.outputs.length} files in ${Math.round(performance.now() - started)}ms`);
  return true;
}

const ok = await build();
if (process.argv.includes("--watch")) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  watch(join(root, "src"), { recursive: true }, () => {
    clearTimeout(timer);
    timer = setTimeout(build, 80);
  });
  console.log("watching src/ for changes…");
} else if (!ok) {
  process.exit(1);
}
