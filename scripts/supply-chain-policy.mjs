import { createHash, randomUUID } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readdir, readFile, stat, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const allowedLicenses = new Set(["0BSD", "Apache-2.0", "BlueOak-1.0.0", "BSD-2-Clause", "BSD-3-Clause", "CC-BY-4.0", "ISC", "MIT", "MPL-2.0", "OFL-1.1", "Python-2.0"]);
const allowedWebRuntimeDependencies = new Set([
  "@fontsource-variable/geist",
  "@fontsource-variable/merriweather",
  "@fontsource-variable/outfit",
  "class-variance-authority",
  "clsx",
  "cmdk",
  "lucide-react",
  "radix-ui",
  "react",
  "react-dom",
  "sonner",
  "tailwind-merge",
  "tw-animate-css",
]);
const allowedWebDevDependencies = new Set(["@playwright/test", "@tailwindcss/vite", "@types/node", "@types/react", "@types/react-dom", "@vitejs/plugin-react", "shadcn", "tailwindcss", "typescript", "vite"]);
const allowedGoDirectDependencies = new Set([
  "github.com/getkin/kin-openapi",
  "github.com/jackc/pgx/v5",
  "github.com/sqlc-dev/sqlc",
  "golang.org/x/sys",
]);
const requiredLockfiles = ["go.sum", "web/app/bun.lock"];
const cdnPattern = /https?:\/\/(?:cdn|unpkg|esm\.sh|jsdelivr|cdnjs)\./i;
const lifecycleScripts = new Set(["preinstall", "install", "postinstall", "prepare", "prepublish", "prepublishOnly"]);

function fail(message) {
  throw new Error(message);
}

async function readJSON(path) {
  return JSON.parse(await readFile(join(root, path), "utf8"));
}

async function assertRequiredFiles() {
  for (const path of requiredLockfiles) {
    if (!existsSync(join(root, path))) {
      fail(`required lockfile is missing: ${path}`);
    }
  }
  if (!existsSync(join(root, "docs/dependency-exceptions.md"))) {
    fail("dependency exceptions document is missing: docs/dependency-exceptions.md");
  }
}

async function assertExceptionsDocumented() {
  const content = await readFile(join(root, "docs/dependency-exceptions.md"), "utf8");
  const required = [...allowedGoDirectDependencies, ...allowedWebRuntimeDependencies, ...allowedWebDevDependencies];
  const missing = required.filter((name) => !content.includes(`\`${name}\``));
  if (missing.length > 0) {
    fail(`dependency exceptions document is missing reviewed dependencies: ${missing.join(", ")}`);
  }
}

async function assertWebPolicy() {
  const pkg = await readJSON("web/app/package.json");
  if (pkg.packageManager !== "bun@1.3.6") {
    fail(`web/app/package.json must pin packageManager to bun@1.3.6, found ${pkg.packageManager ?? "missing"}`);
  }
  const runtimeDeps = Object.keys(pkg.dependencies ?? {});
  const unexpectedRuntime = runtimeDeps.filter((name) => !allowedWebRuntimeDependencies.has(name));
  if (unexpectedRuntime.length > 0) {
    fail(`unexpected frontend runtime dependencies: ${unexpectedRuntime.join(", ")}`);
  }
  const devDeps = Object.keys(pkg.devDependencies ?? {});
  const unexpected = devDeps.filter((name) => !allowedWebDevDependencies.has(name));
  if (unexpected.length > 0) {
    fail(`unexpected frontend dev dependencies: ${unexpected.join(", ")}`);
  }
  const scripts = pkg.scripts ?? {};
  const blocked = Object.keys(scripts).filter((name) => lifecycleScripts.has(name));
  if (blocked.length > 0) {
    fail(`package lifecycle scripts are disabled by policy: ${blocked.join(", ")}`);
  }
  if (Object.keys(pkg.trustedDependencies ?? {}).length > 0 || (pkg.trustedDependencies ?? []).length > 0) {
    fail("Bun trustedDependencies must remain empty unless documented as an exception");
  }
}

function goList(args) {
  const env = { ...process.env, GOCACHE: join(root, ".cache/go-build") };
  return execFileSync("go", args, { cwd: root, env, encoding: "utf8" });
}

async function assertGoPolicy() {
  const direct = goList(["list", "-m", "-f", "{{if not .Main}}{{if not .Indirect}}{{.Path}}{{end}}{{end}}", "all"])
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
  const unexpected = direct.filter((name) => !allowedGoDirectDependencies.has(name));
  if (unexpected.length > 0) {
    fail(`unexpected direct Go dependencies: ${unexpected.join(", ")}`);
  }
}

async function walkFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules" || entry.name === ".git" || entry.name === ".cache" || entry.name === "bin") {
        continue;
      }
      files.push(...(await walkFiles(path)));
      continue;
    }
    files.push(path);
  }
  return files;
}

async function assertNoCDNs() {
  const roots = ["cmd", "db", "docs", "internal", "web", "openapi.yaml", "Dockerfile", "compose.yaml"];
  for (const item of roots) {
    const path = join(root, item);
    if (!existsSync(path)) {
      continue;
    }
    const itemStat = await stat(path);
    const files = itemStat.isDirectory() ? await walkFiles(path) : [path];
    for (const file of files) {
      const content = await readFile(file, "utf8");
      if (cdnPattern.test(content)) {
        fail(`CDN URL found in ${relative(root, file)}`);
      }
    }
  }
}

function normalizeLicense(value) {
  if (!value) {
    return "";
  }
  return String(value).replace(/[()]/g, "").split(/\s+OR\s+|\s+AND\s+/i)[0].trim();
}

async function detectGoLicense(moduleDir) {
  const names = await readdir(moduleDir);
  const licenseFile = names.find((name) => /^licen[cs]e/i.test(name) || /^copying/i.test(name));
  if (!licenseFile) {
    return "";
  }
  const content = (await readFile(join(moduleDir, licenseFile), "utf8")).toLowerCase();
  if (content.includes("mit license") || (content.includes("permission is hereby granted, free of charge") && content.includes("the software is provided \"as is\""))) {
    return "MIT";
  }
  if (content.includes("apache license") && content.includes("version 2.0")) {
    return "Apache-2.0";
  }
  if (content.includes("redistribution and use in source and binary forms")) {
    return "BSD-3-Clause";
  }
  return "UNKNOWN";
}

async function nodePackages() {
  const nodeModules = join(root, "web/app/node_modules");
  if (!existsSync(nodeModules)) {
    fail("web/app/node_modules is missing; run bun install before policy");
  }
  const packages = [];
  for (const entry of await readdir(nodeModules, { withFileTypes: true })) {
    if (!entry.isDirectory()) {
      continue;
    }
    if (entry.name.startsWith("@")) {
      for (const scoped of await readdir(join(nodeModules, entry.name), { withFileTypes: true })) {
        if (scoped.isDirectory()) {
          packages.push(join(nodeModules, entry.name, scoped.name));
        }
      }
      continue;
    }
    packages.push(join(nodeModules, entry.name));
  }
  return packages;
}

async function collectNodeComponents() {
  const components = [];
  for (const dir of await nodePackages()) {
    const packagePath = join(dir, "package.json");
    if (!existsSync(packagePath)) {
      continue;
    }
    const pkg = JSON.parse(await readFile(packagePath, "utf8"));
    const license = normalizeLicense(pkg.license);
    if (!allowedLicenses.has(license)) {
      fail(`unapproved npm package license for ${pkg.name}: ${pkg.license ?? "missing"}`);
    }
    components.push({
      type: "library",
      ecosystem: "npm",
      name: pkg.name,
      version: pkg.version,
      licenses: [{ license: { id: license } }],
      purl: `pkg:npm/${encodeURIComponent(pkg.name).replace("%40", "@")}@${pkg.version}`,
      path: relative(root, dir),
    });
  }
  return components.sort((a, b) => a.name.localeCompare(b.name));
}

async function collectGoComponents() {
  const output = goList(["list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}", "./..."]);
  const seen = new Map();
  for (const line of output.split("\n")) {
    if (!line.trim()) {
      continue;
    }
    const [name, version, dir] = line.split("|");
    if (!seen.has(`${name}@${version}`)) {
      seen.set(`${name}@${version}`, { name, version, dir });
    }
  }
  const components = [];
  for (const component of seen.values()) {
    const license = await detectGoLicense(component.dir);
    if (!allowedLicenses.has(license)) {
      fail(`unapproved Go module license for ${component.name}: ${license || "missing"}`);
    }
    components.push({
      type: "library",
      ecosystem: "go",
      name: component.name,
      version: component.version,
      licenses: [{ license: { id: license } }],
      purl: `pkg:golang/${component.name}@${component.version}`,
      path: component.dir,
    });
  }
  return components.sort((a, b) => a.name.localeCompare(b.name));
}

async function writeArtifacts(components) {
  const artifactDir = join(root, "artifacts");
  await mkdir(artifactDir, { recursive: true });
  const sbom = {
    bomFormat: "CycloneDX",
    specVersion: "1.5",
    serialNumber: `urn:uuid:${randomUUID()}`,
    version: 1,
    metadata: {
      timestamp: new Date().toISOString(),
      component: {
        type: "application",
        name: "NeroCD",
        version: "0.1.0-dev",
      },
    },
    components,
  };
  const sbomPath = join(artifactDir, "sbom.json");
  await writeFile(sbomPath, `${JSON.stringify(sbom, null, 2)}\n`);

  const checksumInputs = ["bin/nerocd", "artifacts/sbom.json", "web/dist/index.html"];
  const lines = [];
  for (const file of checksumInputs) {
    const path = join(root, file);
    if (!existsSync(path)) {
      continue;
    }
    const hash = createHash("sha256").update(await readFile(path)).digest("hex");
    lines.push(`${hash}  ${file}`);
  }
  await writeFile(join(artifactDir, "checksums.txt"), `${lines.join("\n")}\n`);
}

await assertRequiredFiles();
await assertExceptionsDocumented();
await assertWebPolicy();
await assertGoPolicy();
await assertNoCDNs();
const components = [...(await collectGoComponents()), ...(await collectNodeComponents())];
await writeArtifacts(components);
console.log(`ok supply-chain policy (${components.length} components)`);
