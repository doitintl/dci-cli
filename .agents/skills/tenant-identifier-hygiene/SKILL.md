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
3. Stage the intended files so new files are included, then run from the repo
   root (replace `origin/main` with the repository's base branch):

   ```sh
   python3 .agents/skills/tenant-identifier-hygiene/scripts/check_identifiers.py --base origin/main
   python3 -m unittest discover -s .agents/skills/tenant-identifier-hygiene/scripts -p 'test_*.py'
   ```

4. Review every outgoing commit as well as the final diff. Adding a value and
   deleting it in a later commit still publishes it in Git history. If a value
   was already pushed, notify the incident/repository owner; a cleanup commit
   does not erase history, release artifacts, caches or published pages.
5. For generated or CMS content, preview and inspect the rendered result and
   source fields before publishing; verify the public page afterward. Repository
   CI cannot inspect a direct CMS edit. Keep findings to file/page locations and
   remediation, without quoting the values.

## Automated check and limits

`scripts/check_identifiers.py` compares newly introduced identifier occurrences
and filenames against SHA-256 fingerprints of known restricted identifiers. It
checks all paths and extensions, including hidden files, docs, tests and notebooks,
without a directory allowlist. Same-file, count-preserving formatting, comment,
line-ending and reorder changes remain existing configuration; copies, moves to a
new path and additional occurrences are still checked. Existing restricted
filenames can be cleaned up, but a newly introduced or renamed path is checked.
The scanner recognizes plain text, URL/HTML escapes, common JSON/hex escapes, and
domain case/subdomains. It reports locations only and fails if Git/configuration
cannot be read.

This is a regression check, not a classifier for every customer's ID. It does not
detect unknown IDs, arbitrary encodings or values visible only in images, nor
does the final diff erase or validate previous commits. Human/agent review above
is required even when the check passes.

For another repository, copy this skill with its scripts and wire the check into
that repository's PR CI using its base/head commits. Keep the fingerprint set in
sync across adopters, never add the source values alongside it, and use synthetic
data to test the scanner. A CI job becomes a merge gate only when included in an
existing required aggregate check or made required by repository rules.
