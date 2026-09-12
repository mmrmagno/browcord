import * as esbuild from "esbuild";
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { basename } from "node:path";

const clientId = process.env.CLIENT_ID ?? "";
if (!clientId) {
  console.warn("CLIENT_ID is unset: builds will only run outside Discord");
}

const targets = [
  { entry: "src/app/main.ts", html: "src/app/index.html", icon: "src/app/icon.svg", out: "dist" },
];

for (const t of targets) {
  mkdirSync(t.out, { recursive: true });
  copyFileSync(t.icon, `${t.out}/icon.svg`);

  const result = await esbuild.build({
    entryPoints: [t.entry],
    bundle: true,
    format: "esm",
    target: "es2022",
    outdir: t.out,
    entryNames: "app-[hash]",
    sourcemap: true,
    metafile: true,
    define: { __CLIENT_ID__: JSON.stringify(clientId) },
  });

  const jsOut = Object.keys(result.metafile.outputs).find((f) => f.endsWith(".js"));
  const bundle = basename(jsOut);

  const html = readFileSync(t.html, "utf8").replace('src="app.js"', `src="${bundle}"`);
  writeFileSync(`${t.out}/index.html`, html);

  console.log(`built ${t.out}/${bundle}`);
}
