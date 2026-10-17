import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const contract = JSON.parse(readFileSync(new URL("./features.json", import.meta.url), "utf8"));
const check = process.argv.includes("--check");
const title = (name) => name.split(/[._]/).map((part) => part[0].toUpperCase() + part.slice(1)).join("");
const goTypes = { string: "string", number: "float64", boolean: "bool" };
const tsSchema = (shape) => typeof shape === "string"
  ? `schema.${shape}()`
  : `schema.object({ ${Object.entries(shape).map(([key, type]) => `${JSON.stringify(key)}: ${tsSchema(type)}`).join(", ")} })`;
let ts = 'import { defineContract, schema } from "script-api-bridge";\n\nexport const contract = defineContract({\n  requests: {\n';
let go = 'package features\n\nimport "errors"\n\n';
function goShape(name, shape, input = false) {
  if (shape === "null") return;
  go += `type ${name} struct {\n`;
  for (const [key, type] of Object.entries(shape)) {
    if (!goTypes[type]) throw new Error(`Unsupported Go field type: ${type}`);
    go += `\t${title(key)} ${input ? "*" : ""}${goTypes[type]} \`json:"${key}"\`\n`;
  }
  go += "}\n\n";
  if (input) {
    go += `func (r ${name}) Validate() error {\n`;
    for (const key of Object.keys(shape)) {
      go += `\tif r.${title(key)} == nil {\n\t\treturn errors.New("Missing ${key}")\n\t}\n`;
    }
    go += "\treturn nil\n}\n\n";
  }
}
for (const [name, spec] of Object.entries(contract.requests)) {
  ts += `    ${JSON.stringify(name)}: { request: ${tsSchema(spec.request)}, response: ${tsSchema(spec.response)} },\n`;
  go += `const ${title(name)} = ${JSON.stringify(name)}\n\n`;
  goShape(`${title(name)}Request`, spec.request, true);
  goShape(`${title(name)}Response`, spec.response);
}
ts += "  },\n  events: {\n";
for (const [name, shape] of Object.entries(contract.events)) {
  ts += `    ${JSON.stringify(name)}: ${tsSchema(shape)},\n`;
  go += `const ${title(name)} = ${JSON.stringify(name)}\n\n`;
  goShape(title(name) + "Event", shape);
}
ts += "  },\n});\n";
for (const [path, content] of [["../addon/src/contract.ts", ts], ["../server/internal/features/contract.go", go.trimEnd() + "\n"]]) {
  const url = new URL(path, import.meta.url);
  if (check) {
    if (readFileSync(url, "utf8").replaceAll("\r\n", "\n") !== content) {
      throw new Error(`Contract is out of date: ${fileURLToPath(url)}. Run npm run generate:contracts`);
    }
  } else {
    writeFileSync(url, content);
  }
}
