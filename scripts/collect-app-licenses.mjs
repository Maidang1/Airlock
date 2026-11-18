#!/usr/bin/env node
import { cpSync, existsSync, mkdirSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve, join } from "node:path";

// Collect the licenses of the packages shipped in the macOS MyGo app.
const target = process.argv[2];
if (!target) throw new Error("Usage: node scripts/collect-app-licenses.mjs <output-directory>");
const root = resolve(import.meta.dirname, "..");
mkdirSync(target, { recursive: true });
for (const file of ["LICENSE", "NOTICE", "THIRD_PARTY_LICENSES"]) {
  cpSync(join(root, file), join(target, file));
}
cpSync(join(root, "internal/exitpolicy/data"), join(target, "exit-country-data"), { recursive: true });

const novnc = join(root, "third_party/novnc");
for (const file of ["LICENSE.txt", "AUTHORS", "NOTICE.cage", "docs", "vendor/pako/LICENSE"]) {
  if (file.includes("/")) {
    mkdirSync(join(target, "novnc", file.slice(0, file.lastIndexOf("/"))), { recursive: true });
  } else {
    mkdirSync(join(target, "novnc"), { recursive: true });
  }
  cpSync(join(novnc, file), join(target, "novnc", file), { recursive: true });
}
// MPL-covered source and local modifications travel with the executable.
const patchedNovnc = join(root, "_output/novnc-source");
cpSync(existsSync(patchedNovnc) ? patchedNovnc : novnc, join(target, "novnc-source"), { recursive: true });

// Go modules used by the MyGo desktop client (including github.com/egoist/mygo).
const desktopModules = new Set();
const goListDirs = execFileSync("go", ["list", "-deps", "-f", "{{if .Module}}{{.Module.Dir}}{{end}}", "./cmd/cage-desktop"], {
  cwd: root,
  env: { ...process.env, GOTOOLCHAIN: "auto", GOOS: "darwin", GOARCH: "arm64", CGO_ENABLED: "1" },
  encoding: "utf8",
});
for (const dir of goListDirs.split("\n").filter(Boolean)) {
  if (resolve(dir) !== root && existsSync(dir)) desktopModules.add(dir);
}
const desktopNotices = [];
for (const dir of desktopModules) {
  const normalized = dir.replaceAll("\\", "/");
  const idx = normalized.lastIndexOf("/pkg/mod/");
  const name = idx >= 0 ? normalized.slice(idx + 9) : normalized;
  const dest = join(target, "desktop-go", name);
  const licenses = readdirSync(dir).filter(
    (file) => /^(licen[sc]e|copying|notice|patents|authors)(\.|$)/i.test(file) && statSync(join(dir, file)).isFile(),
  );
  if (licenses.length) {
    mkdirSync(dest, { recursive: true });
    for (const file of licenses) cpSync(join(dir, file), join(dest, file));
    desktopNotices.push(name);
  }
}
writeFileSync(join(target, "desktop-go", "NOTICE.txt"), desktopNotices.sort().join("\n") + "\n");

// The embedded Tailscale node is a separate guest executable and module.
const tailnetModules = new Set();
for (const [os, arch] of [["linux", "amd64"], ["darwin", "arm64"]]) {
  const dirs = execFileSync("go", ["list", "-deps", "-f", "{{if .Module}}{{.Module.Dir}}{{end}}", "."], {
    cwd: join(root, "tools/tailnet"),
    env: { ...process.env, GOTOOLCHAIN: "auto", GOOS: os, GOARCH: arch, CGO_ENABLED: "0", GOFLAGS: "" },
    encoding: "utf8",
  });
  for (const dir of dirs.split("\n").filter(Boolean)) {
    if (resolve(dir) !== join(root, "tools/tailnet")) tailnetModules.add(dir);
  }
}
const tailnetNotices = [];
for (const dir of tailnetModules) {
  const normalized = dir.replaceAll("\\", "/");
  const name = normalized.slice(normalized.lastIndexOf("/pkg/mod/") + 9);
  const dest = join(target, "tailnet-go", name);
  const licenses = readdirSync(dir).filter(
    (file) => /^(licen[sc]e|copying|notice|patents|authors)(\.|$)/i.test(file) && statSync(join(dir, file)).isFile(),
  );
  if (!licenses.length) throw Error(`Missing guest dependency license: ${name}`);
  mkdirSync(dest, { recursive: true });
  for (const file of licenses) cpSync(join(dir, file), join(dest, file));
  tailnetNotices.push(name);
}
writeFileSync(join(target, "tailnet-go", "NOTICE.txt"), tailnetNotices.sort().join("\n") + "\n");
