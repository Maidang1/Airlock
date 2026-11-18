import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, rmSync } from "node:fs";
import { readFile, readdir, stat, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const surfaceSrc = join(root, "web-surface");
const outDir = join(root, "internal/workbench/web");
const novncSrc = join(root, "third_party/novnc");
const novncStage = join(root, "_output/novnc-source");

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });
cpSync(surfaceSrc, outDir, { recursive: true });

rmSync(novncStage, { recursive: true, force: true });
mkdirSync(novncStage, { recursive: true });
cpSync(novncSrc, novncStage, { recursive: true });

execFileSync("python3", [join(root, "internal/cage/novnc-patch.py"), novncStage], {
  stdio: "inherit",
});

const novncOut = join(outDir, "novnc");
mkdirSync(novncOut, { recursive: true });
cpSync(join(novncStage, "core"), join(novncOut, "core"), { recursive: true });
cpSync(join(novncStage, "vendor"), join(novncOut, "vendor"), { recursive: true });
const orig = join(novncOut, "core/rfb.cage-original.js");
if (existsSync(orig)) rmSync(orig);

async function verifyAndWriteManifest(directory) {
  const files = {};
  async function inspect(relative = "") {
    for (const entry of await readdir(join(directory, relative), { withFileTypes: true })) {
      const name = [relative, entry.name].filter(Boolean).join("/");
      if (entry.isDirectory()) {
        await inspect(name);
        continue;
      }
      if (name === "README" || name === "build-integrity.json") continue;
      const bytes = await readFile(join(directory, name));
      if (bytes.length === 0) throw new Error(`Empty build resource: ${name}`);
      if (/\.(?:js|css|html)$/.test(name)) {
        if (bytes.includes(0)) throw new Error(`NUL bytes in build resource: ${name}`);
        if (name.endsWith(".js")) {
          const parsed = spawnSync(process.execPath, ["--check", "--input-type=module"], {
            input: bytes,
            encoding: "utf8",
          });
          if (parsed.error) throw parsed.error;
          if (parsed.status !== 0) {
            throw new Error(`Invalid JavaScript build resource: ${name}\n${parsed.stderr}`);
          }
        }
      }
      files[name] = createHash("sha256").update(bytes).digest("hex");
    }
  }
  await inspect();
  for (const entry of ["index.html", "mobile.html", "vnc-surface.html"]) {
    const html = await readFile(join(directory, entry), "utf8");
    const scripts = [...html.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["']/gi)];
    if (!scripts.length) throw new Error(`Missing entry script: ${entry}`);
    for (const match of html.matchAll(/<(?:script|link)\b[^>]*\b(?:src|href)=["']([^"']+)["']/gi)) {
      const url = new URL(match[1], "http://cage.local/" + entry);
      if (url.origin !== "http://cage.local") throw new Error(`External build resource: ${match[1]}`);
      const resource = decodeURIComponent(url.pathname).slice(1);
      if (!files[resource] || !(await stat(join(directory, resource))).isFile()) {
        throw new Error(`Missing build resource: ${resource}`);
      }
    }
  }
  for (const [name] of Object.entries(files)) {
    if (!name.startsWith("assets/") || !name.endsWith(".js")) continue;
    const source = await readFile(join(directory, name), "utf8");
    for (const match of source.matchAll(/^\s*import\s+(?:[\s\S]*?\s+from\s+)?["']([^"']+)["']/gm)) {
      const spec = match[1];
      if (!spec.startsWith("./") && !spec.startsWith("../") && !spec.startsWith("/")) {
        throw new Error(`External ESM import in ${name}: ${spec}`);
      }
      const targetUrl = new URL(spec, "http://cage.local/" + name);
      const resource = decodeURIComponent(targetUrl.pathname).slice(1);
      if (!files[resource] || !(await stat(join(directory, resource))).isFile()) {
        throw new Error(`Missing ESM import target ${resource} referenced by ${name}`);
      }
    }
  }
  await writeFile(join(directory, "build-integrity.json"), JSON.stringify({ version: 1, files }, null, 2) + "\n");
  return Object.keys(files).length;
}

const count = await verifyAndWriteManifest(outDir);
console.log(`Built and verified ${count} web-surface resources.`);
