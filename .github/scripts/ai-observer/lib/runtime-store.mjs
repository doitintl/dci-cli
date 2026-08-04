import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { isAbsolute, join, relative, resolve } from "node:path";

const runtimeFileNames = new Set(["baseline.json", "staged-turn.json", "turn-files.json"]);

const pathWithin = (basePath, parts, message) => {
  const base = resolve(basePath);
  const target = resolve(base, join(...parts));
  const rel = relative(base, target);
  if (rel.startsWith("..") || isAbsolute(rel)) {
    throw new Error(message);
  }
  return target;
};

const repoLocalPath = (repoRoot, ...parts) => pathWithin(repoRoot, parts, "ai-observer: refusing path outside repo");

const runtimeFileName = (name) => {
  if (!runtimeFileNames.has(name)) {
    throw new Error(`ai-observer: refusing unsupported runtime file: ${name}`);
  }
  return name;
};

const aiObserverDir = (repoRoot) => repoLocalPath(repoRoot, ".ai-observer");

const observerPath = (repoRoot, name) =>
  pathWithin(aiObserverDir(repoRoot), [runtimeFileName(name)], `ai-observer: refusing path outside repo: ${name}`);

const readRuntimeJson = (repoRoot, name) => {
  const base = resolve(repoRoot);
  const target = observerPath(repoRoot, name);
  const resolved = resolve(target);
  const rel = relative(base, resolved);
  if (rel.startsWith("..") || isAbsolute(rel)) {
    return undefined;
  }
  return JSON.parse(readFileSync(resolved, "utf8"));
};

export const findRepoRoot = (cwd = process.cwd()) => {
  try {
    return execFileSync("git", ["rev-parse", "--show-toplevel"], {
      cwd,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return undefined;
  }
};

export const currentHeadSha = (repoRoot) => {
  try {
    return execFileSync("git", ["rev-parse", "HEAD"], {
      cwd: repoRoot,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return undefined;
  }
};

export const changeTypeFromGitStatus = (status) => {
  switch (status.charAt(0)) {
    case "A":
      return "added";
    case "D":
      return "deleted";
    case "M":
      return "modified";
    case "R":
      return "renamed";
    default:
      return "unknown";
  }
};

export const normaliseRemote = (remote) => {
  if (!remote) {
    return undefined;
  }
  const githubMatch = remote.match(/github\.com(?:-[^:/]+)?[:/](?<owner>[^/]+)\/(?<repo>[^/.]+)(?:\.git)?$/);
  if (githubMatch?.groups) {
    return `${githubMatch.groups.owner}/${githubMatch.groups.repo}`;
  }
  return remote;
};

export const repositoryFromGit = (cwd = process.cwd()) => {
  try {
    const remote = execFileSync("git", ["remote", "get-url", "origin"], {
      cwd,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
    return normaliseRemote(remote);
  } catch {
    return undefined;
  }
};

const turnFilesPath = (repoRoot) => observerPath(repoRoot, "turn-files.json");

export const readTurnFiles = (repoRoot) => {
  try {
    const parsed = readRuntimeJson(repoRoot, "turn-files.json");
    return new Set(Array.isArray(parsed?.paths) ? parsed.paths.filter((path) => typeof path === "string") : []);
  } catch {
    return new Set();
  }
};

export const appendTurnFile = (repoRoot, path) => {
  if (!path || typeof path !== "string") {
    return;
  }
  const existing = readTurnFiles(repoRoot);
  existing.add(path);
  const dir = aiObserverDir(repoRoot);
  const rel = relative(repoRoot, dir);
  if (rel.startsWith("..") || isAbsolute(rel)) {
    throw new Error("ai-observer: refusing to write outside repo");
  }
  mkdirSync(dir, { recursive: true });
  const tmp = join(dir, `turn-files.${process.pid}-${Date.now()}.tmp`);
  writeFileSync(tmp, JSON.stringify({ paths: [...existing] }));
  renameSync(tmp, turnFilesPath(repoRoot));
};

export const clearTurnFiles = (repoRoot) => {
  rmSync(turnFilesPath(repoRoot), { force: true });
};

const stagedTurnPath = (repoRoot) => observerPath(repoRoot, "staged-turn.json");

export const hasStagedTurn = (repoRoot) => {
  try {
    const parsed = readRuntimeJson(repoRoot, "staged-turn.json");
    return Boolean(parsed?.stagedAt);
  } catch {
    return false;
  }
};

export const markTurnStaged = (repoRoot, provider) => {
  const dir = aiObserverDir(repoRoot);
  const rel = relative(repoRoot, dir);
  if (rel.startsWith("..") || isAbsolute(rel)) {
    throw new Error("ai-observer: refusing to write outside repo");
  }
  mkdirSync(dir, { recursive: true });
  const tmp = join(dir, `staged-turn.${process.pid}-${Date.now()}.tmp`);
  writeFileSync(tmp, JSON.stringify({ provider, stagedAt: new Date().toISOString() }));
  renameSync(tmp, stagedTurnPath(repoRoot));
};

export const clearStagedTurn = (repoRoot) => {
  rmSync(stagedTurnPath(repoRoot), { force: true });
};

const baselineFilePath = (repoRoot) => observerPath(repoRoot, "baseline.json");

const fileSignature = (absolutePath) => {
  try {
    const stat = statSync(absolutePath);
    return `${stat.mtimeMs}:${stat.size}`;
  } catch {
    return "deleted";
  }
};

export const computeDirtyState = (repoRoot) => {
  const state = {};
  const git = (...args) => {
    try {
      return execFileSync("git", args, {
        cwd: repoRoot,
        encoding: "utf8",
        stdio: ["ignore", "pipe", "ignore"],
      }).trim();
    } catch {
      return undefined;
    }
  };

  const base = resolve(repoRoot);
  const recordPath = (rawPath) => {
    const target = resolve(base, rawPath);
    const rel = relative(base, target);
    if (rel.startsWith("..") || isAbsolute(rel) || rel === ".ai-observer" || rel.startsWith(".ai-observer/")) {
      return;
    }
    state[rawPath] = fileSignature(target);
  };

  const diff = git("diff", "--name-status", "HEAD");
  for (const line of diff?.split("\n") ?? []) {
    if (!line.trim()) {
      continue;
    }
    const [, firstPath, secondPath] = line.split("\t");
    const path = secondPath ?? firstPath;
    if (path) {
      recordPath(path);
    }
  }

  const untracked = git("ls-files", "--others", "--exclude-standard");
  for (const path of untracked?.split("\n") ?? []) {
    const trimmed = path.trim();
    if (trimmed) {
      recordPath(trimmed);
    }
  }

  return state;
};

export const readBaseline = (repoRoot) => {
  try {
    const parsed = readRuntimeJson(repoRoot, "baseline.json");
    return parsed?.paths && typeof parsed.paths === "object" ? parsed : undefined;
  } catch {
    return undefined;
  }
};

export const writeBaseline = (repoRoot, paths, headSha = currentHeadSha(repoRoot)) => {
  const dir = aiObserverDir(repoRoot);
  const rel = relative(repoRoot, dir);
  if (rel.startsWith("..") || isAbsolute(rel)) {
    throw new Error("ai-observer: refusing to write outside repo");
  }
  mkdirSync(dir, { recursive: true });
  const tmp = join(dir, `baseline.${process.pid}-${Date.now()}.tmp`);
  writeFileSync(tmp, JSON.stringify({ recordedAt: new Date().toISOString(), headSha, paths }));
  renameSync(tmp, baselineFilePath(repoRoot));
};

export const clearBaseline = (repoRoot) => {
  rmSync(baselineFilePath(repoRoot), { force: true });
};

export const pathsChangedSinceBaseline = (current, baseline) => {
  if (!baseline?.paths) {
    return [];
  }
  const changed = [];
  for (const [path, sig] of Object.entries(current)) {
    if (baseline.paths[path] !== sig) {
      changed.push(path);
    }
  }
  return changed;
};
