#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

import { filesChangedFromCommit } from "./lib/commit-files.mjs";
import { validEmail, validGithubId } from "./lib/identity.mjs";
import { normaliseRemote } from "./lib/runtime-store.mjs";

export { validEmail } from "./lib/identity.mjs";

const cloudRunUrl = process.env.AI_OBSERVER_CLOUD_RUN_URL ?? "https://ai-observer-mcp-772991852481.us-central1.run.app";
const endpoint = process.env.AI_OBSERVER_GITHUB_COMMITS_URL ?? `${cloudRunUrl}/github/commits`;

const git = (...args) => {
  try {
    return execFileSync("git", args, { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
  } catch {
    return undefined;
  }
};

const authToken = () =>
  process.env.AI_OBSERVER_AUTH_TOKEN ?? process.env.AI_OBSERVER_EVENTS_TOKEN ?? process.env.AI_OBSERVER_MCP_TOKEN;

const githubToken = () => process.env.GITHUB_TOKEN;

export const githubActorIdentity = (user) => {
  const githubId = validGithubId(user?.id);
  return {
    ...(user?.login ? { actorUserId: user.login } : {}),
    ...(githubId ? { actorGithubId: githubId } : {}),
  };
};

export const githubApi = async (path) => {
  const repo = repository();
  const token = githubToken();
  if (!repo) {
    throw new Error("GitHub API request failed: repository is unknown");
  }
  if (!token) {
    throw new Error("GitHub API request failed: GITHUB_TOKEN is not configured");
  }

  let response;
  try {
    response = await fetch(`https://api.github.com/repos/${repo}${path}`, {
      headers: {
        accept: "application/vnd.github+json",
        authorization: `Bearer ${token}`,
        "x-github-api-version": "2022-11-28",
        "user-agent": "ai-observer-reconcile",
      },
    });
  } catch (error) {
    throw new Error(`GitHub API request failed for ${path}`, { cause: error });
  }

  if (!response.ok) {
    throw new Error(`GitHub API request failed for ${path}: HTTP ${response.status}`);
  }

  try {
    return await response.json();
  } catch (error) {
    throw new Error(`GitHub API returned invalid JSON for ${path}`, { cause: error });
  }
};

const githubEvent = () => {
  if (!process.env.GITHUB_EVENT_PATH) {
    return {};
  }
  try {
    return JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, "utf8"));
  } catch {
    return {};
  }
};

const repository = () =>
  process.env.AI_OBSERVER_REPOSITORY ??
  process.env.GITHUB_REPOSITORY ??
  normaliseRemote(git("remote", "get-url", "origin"));

const branch = () =>
  process.env.AI_OBSERVER_BRANCH ?? process.env.GITHUB_REF_NAME ?? git("rev-parse", "--abbrev-ref", "HEAD");

const DEFAULT_BASE_BRANCH = "dev";

const baseBranch = () => process.env.AI_OBSERVER_BASE_BRANCH ?? DEFAULT_BASE_BRANCH;

export const isBaseBranchPush = (currentBranch, baseBranchName) =>
  Boolean(currentBranch) && currentBranch === baseBranchName;

export const shouldSkipReconciliationBranch = (currentBranch, baseBranchName) =>
  isBaseBranchPush(currentBranch, baseBranchName) ||
  Boolean(currentBranch?.startsWith(`gh-readonly-queue/${baseBranchName}/`));

const pushedToGeneratedBranch = () => shouldSkipReconciliationBranch(branch(), baseBranch());

const prNumberFromEnv = () => {
  const raw = process.env.AI_OBSERVER_PR_NUMBER;
  if (!raw) {
    return undefined;
  }
  const parsed = Number(raw);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined;
};

export const normalizePullRequest = (pulls, preferredBranch) => {
  const candidates = Array.isArray(pulls) ? pulls.filter((candidate) => candidate?.number && candidate?.title) : [];
  const preferred = preferredBranch
    ? candidates.find((candidate) => candidate.head?.ref === preferredBranch)
    : undefined;
  const pull = preferred ?? candidates[0];
  if (!pull) {
    return undefined;
  }
  return {
    prNumber: pull.number,
    ...(pull.head?.ref ? { headRef: pull.head.ref } : {}),
    task: {
      id: `github-pr-${pull.number}`,
      title: pull.title,
      source: "github",
    },
  };
};

export const resolvePullRequestMetadata = (envPrNumber, pulls, preferredBranch) =>
  normalizePullRequest(pulls, preferredBranch) ?? (envPrNumber ? { prNumber: envPrNumber } : undefined);

export const commitBelongsToBranch = (pullRequest, currentBranch) => {
  const headRef = pullRequest?.headRef;
  if (!headRef || !currentBranch) {
    return true;
  }
  return headRef === currentBranch;
};

const stripHeadRef = (pullRequest) => {
  if (!pullRequest) {
    return undefined;
  }
  const { headRef: _headRef, ...rest } = pullRequest;
  return rest;
};

export const pullRequestForCommit = async (sha, preferredBranch) => {
  const envPrNumber = prNumberFromEnv();
  try {
    return resolvePullRequestMetadata(envPrNumber, await githubApi(`/commits/${sha}/pulls`), preferredBranch);
  } catch (error) {
    process.stderr.write(
      `ai-observer GitHub PR metadata lookup failed for ${sha}; continuing without PR metadata: ${error instanceof Error ? error.message : String(error)}\n`
    );
    return resolvePullRequestMetadata(envPrNumber, undefined, preferredBranch);
  }
};

export const githubIdentityForCommit = async (sha) => {
  try {
    return githubActorIdentity((await githubApi(`/commits/${sha}`))?.author);
  } catch (error) {
    process.stderr.write(
      `ai-observer GitHub identity lookup failed for ${sha}; continuing without GitHub identity: ${error instanceof Error ? error.message : String(error)}\n`
    );
    return {};
  }
};

const commitRangeFromEvent = (event) => {
  if (process.env.AI_OBSERVER_COMMIT_RANGE) {
    return process.env.AI_OBSERVER_COMMIT_RANGE;
  }
  if (event.before && event.after && !/^0+$/.test(event.before)) {
    return `${event.before}..${event.after}`;
  }
  return undefined;
};

const commitShas = (event) => {
  if (process.env.AI_OBSERVER_COMMIT_SHA) {
    return [process.env.AI_OBSERVER_COMMIT_SHA];
  }

  const range = commitRangeFromEvent(event);
  if (range) {
    return git("rev-list", "--reverse", range)?.split("\n").filter(Boolean) ?? [];
  }

  if (Array.isArray(event.commits) && event.commits.length > 0) {
    return event.commits.map((commit) => commit.id).filter(Boolean);
  }

  const after = event.after ?? git("rev-parse", "HEAD");
  return after ? [after] : [];
};

const commitActorEmail = (sha) => git("show", "-s", "--format=%ae", sha);

export const normalizeGitDate = (value) => {
  if (!value) {
    return undefined;
  }
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toISOString();
};

const committedAt = (sha) => normalizeGitDate(git("show", "-s", "--format=%cI", sha));

const parentShas = (sha) => git("show", "-s", "--format=%P", sha)?.split(/\s+/).filter(Boolean) ?? [];

const MAX_COMMITS_PER_RECONCILIATION = 100;
const MAX_FILES_PER_COMMIT = 500;
const MAX_GITHUB_API_CONCURRENCY = 5;

export const mapWithConcurrency = async (values, concurrency, mapper) => {
  const results = new Array(values.length);
  let nextIndex = 0;
  const workers = Array.from({ length: Math.min(concurrency, values.length) }, async () => {
    while (nextIndex < values.length) {
      const index = nextIndex;
      nextIndex += 1;
      results[index] = await mapper(values[index], index);
    }
  });
  await Promise.all(workers);
  return results;
};

const commitPayload = (sha) => {
  const gitAuthorEmail = commitActorEmail(sha);

  return {
    commitSha: sha,
    parentShas: parentShas(sha),
    ...(validEmail(gitAuthorEmail) ? { actorEmail: gitAuthorEmail } : {}),
    committedAt: committedAt(sha),
    filesChanged: filesChangedFromCommit(sha).slice(0, MAX_FILES_PER_COMMIT),
  };
};

export const withPullRequestMetadata = (commit, pullRequest) => ({
  ...commit,
  ...(pullRequest ?? {}),
});

export const withActorIdentity = (commit, actorIdentity) => ({
  ...commit,
  ...actorIdentity,
});

const postReconciliation = async (body) => {
  const token = authToken();
  const response = await fetch(endpoint, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-ai-client": "github_action",
      ...(token ? { authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(`AI observer GitHub reconciliation returned ${response.status}: ${text}`);
  }

  return response.json().catch(() => ({ status: "reconciled" }));
};

const main = async () => {
  const event = githubEvent();
  const repo = repository();
  if (!repo) {
    process.stderr.write("ai-observer GitHub reconciliation skipped: repository is unknown.\n");
    return;
  }

  const currentBranch = branch();
  if (pushedToGeneratedBranch()) {
    process.stdout.write(
      `ai-observer GitHub reconciliation skipped: push to ${currentBranch} is generated from source-branch work.\n`
    );
    return;
  }

  const commitPayloads = commitShas(event)
    .slice(0, MAX_COMMITS_PER_RECONCILIATION)
    .map(commitPayload)
    .filter((commit) => commit.filesChanged.length > 0);

  const commitsWithMetadata = await mapWithConcurrency(commitPayloads, MAX_GITHUB_API_CONCURRENCY, async (commit) => {
    const pullRequest = await pullRequestForCommit(commit.commitSha, currentBranch);
    if (!commitBelongsToBranch(pullRequest, currentBranch)) {
      return undefined;
    }
    return withActorIdentity(
      withPullRequestMetadata(commit, stripHeadRef(pullRequest)),
      await githubIdentityForCommit(commit.commitSha)
    );
  });

  const commits = commitsWithMetadata.filter(Boolean);

  if (!commits.length) {
    process.stdout.write("ai-observer GitHub reconciliation skipped: no changed commits.\n");
    return;
  }

  const result = await postReconciliation({
    repository: repo,
    branch: currentBranch,
    commits,
  });

  process.stdout.write(`${JSON.stringify(result)}\n`);
};

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch((error) => {
    process.stderr.write(
      `ai-observer GitHub reconciliation failed: ${error instanceof Error ? error.message : String(error)}\n`
    );
    process.exit(1);
  });
}
