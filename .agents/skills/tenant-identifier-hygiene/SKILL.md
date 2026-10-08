---
name: tenant-identifier-hygiene
description: >-
  Prevent real customer or tenant identifiers and tenant-specific domains from
  entering code, tests, fixtures, documentation, CLI help, examples, generated
  artifacts, or published content. Use when authoring or reviewing those changes
  in private or public repositories, preparing a PR, or publishing help content
  through a CMS such as ReadMe.
---

# Tenant identifier hygiene

Use synthetic identities for examples and generic tests, including internal
tenants. A private repository, an existing occurrence, or a publicly discoverable
value is not permission to copy it. IDs are not credentials, but publishing them
can disclose tenant identity. Do not reproduce real values in this skill, test
cases, findings, commit messages, PR descriptions, or review comments.

## Choose the right replacement

- Docs, help text, curl examples and URL samples: use `{CUSTOMER_ID}` or the
  format's equivalent placeholder. Use `example.com` or `console.example.com`
  for tenant-specific domains; keep official product/API endpoint hostnames.
- Generic tests: create named synthetic customers such as `test-customer-a` and
  `test-customer-b`. Keep separate identities for isolation scenarios. If a
  validator requires a specific shape, generate deterministic synthetic values
  of that shape and make their fixture purpose clear.
- Mock the tenant lookup or internal-customer predicate when exercising generic
  business behavior. Do not import a production customer constant merely to
  make a generic fixture pass, and do not remove positive/negative cases.
- Tests specifically proving a production allowlist or tenant-routing contract
  may refer to its existing shared constant without duplicating the value.
- Required runtime configuration is different: preserve routing, authorization,
  billing and allowlist behavior. Reuse existing canonical constants or obtain
  configuration at runtime. Never replace a required production value with a
  fake to satisfy the check. New literal configuration needs owner review and
  a narrowly justified policy change, not a blanket directory exemption.

## Before committing or publishing

1. Inspect changed source and its outputs: docs (including private docs), test
   snapshots, fixtures, notebooks with saved outputs, CLI help, OpenAPI examples,
   scripts, generated reports, screenshots, recordings, URLs and filenames.
   A generator importing a real constant can still expose its resolved value.
2. Sanitize captured API data and URLs before saving them. Keep required live
   integration-test identities in injected environment/configuration, never in
   committed examples or saved output. Check that ignored evidence is actually
   ignored before writing it; do not publish that evidence later.
3. Stage the intended files so new files are included, then inspect the complete
   staged diff and filenames from the repository root. Repeat the review after
   regenerating fixtures, documentation, screenshots or other derived output.

4. Review every outgoing commit as well as the final diff. Adding a value and
   deleting it in a later commit still publishes it in Git history. If a value
   was already pushed, notify the incident/repository owner; a cleanup commit
   does not erase history, release artifacts, caches or published pages.
5. For generated or CMS content, preview and inspect the rendered result and
   source fields before publishing; verify the public page afterward. Repository
   CI cannot inspect a direct CMS edit. Keep findings to file/page locations and
   remediation, without quoting the values.

## Review limits

This repository relies on human and agent review for tenant-identifier hygiene;
CI does not classify or block tenant identifiers. Review the actual values and
their context rather than assuming a pattern or directory is safe. Text review
does not cover values visible only in images, recordings or externally published
CMS content, and a clean final diff does not erase or validate previous commits.

For another repository, copy this guidance and adapt its examples and review
surfaces to that project's workflows. Keep the manual review responsibility
explicit rather than assuming another repository's automation covers it.
