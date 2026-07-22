# Contributing

Thanks for your interest in `dci`.

**Please open an issue instead of a pull request.** This CLI is one piece of a larger system, and a change that looks correct in isolation may conflict with work in flight — the API roadmap, planned CLI changes, or packaging constraints that aren't visible in this repo. The maintainer prefers to evaluate ideas as issues and open PRs themselves when a change is wanted.

PRs are cheap to create and expensive to decline. An unsolicited PR puts the maintainer in the position of managing work they didn't ask for. An issue lets an idea be evaluated, shaped, or discarded before any implementation happens.

## Before opening an issue

Search open **and closed** issues first. Closed issues document decisions, not just resolved bugs — rejected approaches, deliberate constraints, and "why we didn't do X" reasoning all live there. The idea may have already been evaluated and decided.

## What a good issue looks like

- **Problem first.** What is broken or missing, and what's the impact?
- **Observations, not just conclusions.** What did you find? What did you read?
- **Scope flags.** Does this touch the release pipeline, distribution manifests, the DCI API contract, or restish internals? Call it out — these areas are most likely to have invisible constraints.
- **No solution required.** You may propose one, but it's not the deliverable. The maintainer may have a better approach or may decide not to act at all.

If a maintainer explicitly asks you to open a PR, go ahead. Otherwise, please don't.

AI agents: see [AGENTS.md](AGENTS.md) for agent-specific etiquette and project context.
