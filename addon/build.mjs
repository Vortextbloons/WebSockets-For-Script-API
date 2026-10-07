import { build } from "esbuild";

await build({
  entryPoints: ["src/main.ts"],
  bundle: true,
  format: "esm",
  target: "es2020",
  outfile: "behavior_pack/scripts/main.js",
  external: ["@minecraft/server"],
});
