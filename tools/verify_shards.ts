#!/usr/bin/env bun
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, normalize, resolve, sep } from "node:path";

type Backend = "local-api" | "remote-api";

type Args = {
  inputs: string[];
  all: boolean;
  backend?: string;
  endpoint?: string;
  model?: string;
  apiKey?: string;
  jobs?: number;
  timeout?: number;
  force: boolean;
  dryRun: boolean;
};

type Shard = {
  slug: string;
  folder: string;
  path: string;
  responsePath: string;
};

type PlannedShard = {
  shard: Shard;
  skipped: boolean;
  templateIssues: string[];
};

type RunResult = {
  ok: boolean;
};

const REQUIRED_MARKERS = ["# ", "Source:", "Skeleton:", "## Problem", "## Checks", "## Answer Format"];
const BACKENDS = new Set<Backend>(["local-api", "remote-api"]);

function parseArgs(argv: string[]): Args {
  const args: Args = {
    inputs: [],
    all: false,
    backend: process.env.VERIFY_SHARDS_BACKEND,
    endpoint: process.env.VERIFY_SHARDS_ENDPOINT,
    model: process.env.VERIFY_SHARDS_MODEL,
    apiKey: process.env.VERIFY_SHARDS_API_KEY,
    jobs: undefined,
    timeout: undefined,
    force: false,
    dryRun: false,
  };

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--all") args.all = true;
    else if (arg === "--backend") args.backend = argv[++i];
    else if (arg === "--endpoint") args.endpoint = argv[++i];
    else if (arg === "--model") args.model = argv[++i];
    else if (arg === "--api-key") args.apiKey = argv[++i];
    else if (arg === "--jobs") args.jobs = Number.parseInt(argv[++i] ?? "", 10);
    else if (arg === "--timeout") args.timeout = Number.parseFloat(argv[++i] ?? "");
    else if (arg === "--force") args.force = true;
    else if (arg === "--dry-run") args.dryRun = true;
    else if (arg === "--help" || arg === "-h") {
      usage();
      process.exit(0);
    } else if (arg.startsWith("--")) {
      throw new Error(`unknown option: ${arg}`);
    } else {
      args.inputs.push(arg);
    }
  }

  return args;
}

function usage(): void {
  console.log(`verify_shards

Verify proof shards through local or remote OpenAI-compatible APIs.

Usage:
  bun tools/verify_shards.ts --all [options]
  bun tools/verify_shards.ts verify-shards/<slug> [options]
  bun tools/verify_shards.ts verify-shards/<slug>/shard.md [options]

Options:
  --all                 Verify every verify-shards/*/shard.md file.
  --backend <name>      local-api or remote-api.
  --endpoint <url>      OpenAI-compatible endpoint or base URL.
  --model <name>        Model name.
  --api-key <key>       Optional bearer token.
  --jobs <n>            Maximum concurrent API requests. Omit for no script-side cap.
  --timeout <seconds>   Per-request timeout. Omit for no timeout.
  --force               Overwrite existing response.md files.
  --dry-run             Show what would happen without API calls or writes.
`);
}

function fail(message: string): number {
  console.error(`error: ${message}`);
  return 2;
}

function resolveInputs(args: Args): Shard[] {
  if (args.all && args.inputs.length > 0) throw new Error("use either --all or explicit shard inputs, not both");
  if (!args.all && args.inputs.length === 0) throw new Error("provide shard inputs or use --all");

  let paths: string[];
  if (args.all) {
    const shardsDir = resolve("verify-shards");
    if (!isDirectory(shardsDir)) throw new Error("verify-shards/ does not exist");
    paths = readdirSync(shardsDir)
      .map((name) => join(shardsDir, name, "shard.md"))
      .filter((path) => isFile(path))
      .sort();
    if (paths.length === 0) throw new Error("no shards found at verify-shards/*/shard.md");
  } else {
    paths = args.inputs.map((raw) => resolveOneInput(resolve(raw)));
  }

  const seen = new Set<string>();
  const shards: Shard[] = [];
  for (const path of paths) {
    const shard = makeShard(path);
    if (!seen.has(shard.path)) {
      seen.add(shard.path);
      shards.push(shard);
    }
  }
  return shards.sort((a, b) => a.path.localeCompare(b.path));
}

function resolveOneInput(path: string): string {
  if (isDirectory(path)) {
    const candidate = join(path, "shard.md");
    if (!isFile(candidate)) throw new Error(`${path} is a directory but does not contain shard.md`);
    return candidate;
  }
  if (path.split(sep).at(-1) !== "shard.md") throw new Error(`${path} is not a shard folder or shard.md file`);
  if (!isFile(path)) throw new Error(`${path} does not exist`);
  return path;
}

function makeShard(path: string): Shard {
  const folder = dirname(path);
  const parts = normalize(path).split(sep);
  if (parts.at(-1) !== "shard.md") throw new Error(`${path} is not named shard.md`);
  if (parts.at(-3) !== "verify-shards") throw new Error(`${path} is not under verify-shards/<slug>/`);
  return {
    slug: parts.at(-2) ?? "",
    folder,
    path,
    responsePath: join(folder, "response.md"),
  };
}

function isDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

function isFile(path: string): boolean {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

function templateIssues(text: string): string[] {
  const issues: string[] = [];
  let searchFrom = 0;
  for (const marker of REQUIRED_MARKERS) {
    const index = text.indexOf(marker, searchFrom);
    if (index === -1) issues.push(`missing or out-of-order marker: ${marker}`);
    else searchFrom = index + marker.length;
  }
  return issues;
}

function planShards(shards: Shard[], force: boolean): PlannedShard[] {
  return shards.map((shard) => {
    if (existsSync(shard.responsePath) && !force) {
      return { shard, skipped: true, templateIssues: [] };
    }
    const text = readFileSync(shard.path, "utf8");
    return { shard, skipped: false, templateIssues: templateIssues(text) };
  });
}

function normalizeEndpoint(endpoint: string): string {
  const trimmed = endpoint.replace(/\/+$/, "");
  if (trimmed.endsWith("/v1/chat/completions")) return trimmed;
  if (trimmed.endsWith("/v1")) return `${trimmed}/chat/completions`;
  return `${trimmed}/v1/chat/completions`;
}

async function callOpenAICompatible({
  endpoint,
  model,
  apiKey,
  shardText,
  timeout,
}: {
  endpoint: string;
  model: string;
  apiKey?: string;
  shardText: string;
  timeout?: number;
}): Promise<string> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (apiKey) headers.Authorization = `Bearer ${apiKey}`;

  const controller = timeout === undefined ? undefined : new AbortController();
  const timer = timeout === undefined ? undefined : setTimeout(() => controller.abort(), timeout * 1000);

  try {
    const response = await fetch(endpoint, {
      method: "POST",
      headers,
      body: JSON.stringify({
        model,
        messages: [{ role: "user", content: shardText }],
      }),
      signal: controller?.signal,
    });

    const body = await response.text();
    if (!response.ok) throw new Error(`HTTP ${response.status}: ${body.slice(0, 500)}`);

    let data: unknown;
    try {
      data = JSON.parse(body);
    } catch {
      throw new Error(`could not parse JSON response: ${body.slice(0, 500)}`);
    }

    const content = readResponseContent(data);
    if (typeof content !== "string") {
      throw new Error(`could not read choices[0].message.content from response: ${body.slice(0, 500)}`);
    }
    return content;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

function readResponseContent(data: unknown): unknown {
  if (!data || typeof data !== "object") return undefined;
  const choices = (data as { choices?: unknown }).choices;
  if (!Array.isArray(choices)) return undefined;
  const first = choices[0];
  if (!first || typeof first !== "object") return undefined;
  const message = (first as { message?: unknown }).message;
  if (!message || typeof message !== "object") return undefined;
  return (message as { content?: unknown }).content;
}

async function verifyOne(planned: PlannedShard, args: Args, endpoint: string): Promise<Shard> {
  if (!args.model) throw new Error("missing model");
  const shardText = readFileSync(planned.shard.path, "utf8");
  const output = await callOpenAICompatible({
    endpoint,
    model: args.model,
    apiKey: args.apiKey,
    shardText,
    timeout: args.timeout,
  });
  writeFileSync(planned.shard.responsePath, `${output.trimEnd()}\n`, "utf8");
  return planned.shard;
}

async function runVerification(planned: PlannedShard[], args: Args): Promise<number> {
  const skipped = planned.filter((item) => item.skipped);
  const failedTemplate = planned.filter((item) => !item.skipped && item.templateIssues.length > 0);
  const runnable = planned.filter((item) => !item.skipped && item.templateIssues.length === 0);

  for (const item of skipped) console.log(`[skip] ${item.shard.folder} (response.md exists)`);
  for (const item of failedTemplate) {
    console.log(`[template failed] ${item.shard.path}`);
    for (const issue of item.templateIssues) console.log(`  - ${issue}`);
  }

  if (args.dryRun) {
    for (const item of runnable) {
      const action = existsSync(item.shard.responsePath) && args.force ? "would overwrite" : "would verify";
      console.log(`[dry-run] ${action} ${item.shard.path}`);
    }
    printSummary(planned, { completed: 0, executionFailed: 0, dryRun: true });
    return failedTemplate.length > 0 ? 1 : 0;
  }

  if (runnable.length > 0 && (!args.backend || !args.endpoint || !args.model)) {
    const missing: string[] = [];
    if (!args.backend) missing.push("--backend or VERIFY_SHARDS_BACKEND");
    if (!args.endpoint) missing.push("--endpoint or VERIFY_SHARDS_ENDPOINT");
    if (!args.model) missing.push("--model or VERIFY_SHARDS_MODEL");
    printSummary(planned, { completed: 0, executionFailed: 0, dryRun: false });
    console.error(`error: missing ${missing.join(", ")}`);
    return 2;
  }

  if (runnable.length === 0) {
    printSummary(planned, { completed: 0, executionFailed: 0, dryRun: false });
    return failedTemplate.length > 0 ? 1 : 0;
  }

  if (args.jobs !== undefined && (!Number.isInteger(args.jobs) || args.jobs < 1)) {
    printSummary(planned, { completed: 0, executionFailed: 0, dryRun: false });
    console.error("error: --jobs must be at least 1");
    return 2;
  }

  const endpoint = normalizeEndpoint(args.endpoint ?? "");
  const results = args.jobs === undefined
    ? await Promise.all(runnable.map((item) => runOne(item, args, endpoint)))
    : await runPool(runnable, args.jobs, (item) => runOne(item, args, endpoint));

  const completed = results.filter((result) => result.ok).length;
  const executionFailed = results.length - completed;
  printSummary(planned, { completed, executionFailed, dryRun: false });
  return failedTemplate.length > 0 || executionFailed > 0 ? 1 : 0;
}

async function runOne(item: PlannedShard, args: Args, endpoint: string): Promise<RunResult> {
  try {
    const shard = await verifyOne(item, args, endpoint);
    console.log(`[done] ${shard.path} -> ${shard.responsePath}`);
    return { ok: true };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.log(`[execution failed] ${item.shard.path}: ${message}`);
    return { ok: false };
  }
}

async function runPool<T>(items: T[], jobs: number, worker: (item: T) => Promise<RunResult>): Promise<RunResult[]> {
  const results: RunResult[] = [];
  let next = 0;
  async function loop(): Promise<void> {
    while (next < items.length) {
      const item = items[next];
      next += 1;
      results.push(await worker(item));
    }
  }
  await Promise.all(Array.from({ length: Math.min(jobs, items.length) }, loop));
  return results;
}

function printSummary(
  planned: PlannedShard[],
  { completed, executionFailed, dryRun }: { completed: number; executionFailed: number; dryRun: boolean },
): void {
  const skipped = planned.filter((item) => item.skipped).length;
  const templateFailed = planned.filter((item) => !item.skipped && item.templateIssues.length > 0).length;
  const runnable = planned.filter((item) => !item.skipped && item.templateIssues.length === 0).length;

  if (dryRun) {
    console.log(
      `summary selected=${planned.length} skipped=${skipped} ` +
      `would_check=${planned.length - skipped} would_verify=${runnable} template_failed=${templateFailed}`,
    );
  } else {
    console.log(
      `summary selected=${planned.length} skipped=${skipped} template_failed=${templateFailed} ` +
      `queued=${runnable} completed=${completed} execution_failed=${executionFailed}`,
    );
  }
}

async function main(): Promise<number> {
  const args = parseArgs(process.argv.slice(2));
  if (args.backend && !BACKENDS.has(args.backend as Backend)) {
    return fail("--backend must be one of: local-api, remote-api");
  }
  const shards = resolveInputs(args);
  const planned = planShards(shards, args.force);
  return runVerification(planned, args);
}

main()
  .then((code) => process.exit(code))
  .catch((error: unknown) => {
    const message = error instanceof Error ? error.message : String(error);
    process.exit(fail(message));
  });
