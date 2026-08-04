import { execFileSync } from "node:child_process";

import { changeTypeFromGitStatus } from "./runtime-store.mjs";

const git = (...args) => {
  try {
    return execFileSync("git", args, { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
  } catch {
    return undefined;
  }
};

const parseNumstatCount = (value) => {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : 0;
};

const pathFromNumstat = (rawPath) => {
  if (!rawPath) {
    return undefined;
  }
  const braceRename = rawPath.match(/^(?<prefix>.*)\{(?<from>.*) => (?<to>.*)\}(?<suffix>.*)$/);
  if (braceRename?.groups) {
    return `${braceRename.groups.prefix}${braceRename.groups.to}${braceRename.groups.suffix}`;
  }
  if (rawPath.includes(" => ")) {
    return rawPath.split(" => ").pop();
  }
  return rawPath;
};

const lineStatsFromCommit = (commitSha) => {
  const diff = git("diff-tree", "--no-commit-id", "--numstat", "-r", "-M", commitSha);
  const stats = [];
  for (const line of diff?.split("\n") ?? []) {
    if (!line.trim()) {
      continue;
    }
    const [added, deleted, rawPath] = line.split("\t");
    stats.push({
      path: pathFromNumstat(rawPath),
      linesAdded: parseNumstatCount(added),
      linesDeleted: parseNumstatCount(deleted),
    });
  }
  return stats;
};

export const filesChangedFromCommit = (commitSha, attributedFiles = []) => {
  const flagsByPath = new Map();
  for (const file of attributedFiles) {
    if (file?.path) {
      flagsByPath.set(file.path, {
        aiTouched: Boolean(file.aiTouched),
        manuallyTouched: Boolean(file.manuallyTouched),
      });
    }
  }

  const lineStats = lineStatsFromCommit(commitSha);
  const lineStatsByPath = new Map(lineStats.filter((stat) => stat.path).map((stat) => [stat.path, stat]));
  const diff = git("diff-tree", "--no-commit-id", "--name-status", "-r", "-M", commitSha);
  if (!diff) {
    return [];
  }
  const files = [];
  for (const line of diff.split("\n")) {
    if (!line.trim()) {
      continue;
    }
    const [status, firstPath, secondPath] = line.split("\t");
    const path = secondPath ?? firstPath;
    if (path) {
      const flags = flagsByPath.get(path);
      const lineStat = lineStatsByPath.get(path);
      files.push({
        path,
        changeType: changeTypeFromGitStatus(status),
        aiTouched: flags?.aiTouched ?? false,
        manuallyTouched: flags?.manuallyTouched ?? true,
        ...(lineStat ? { linesAdded: lineStat.linesAdded, linesDeleted: lineStat.linesDeleted } : {}),
      });
    }
  }
  return files;
};
