# Agent Friction Notes — "token spend per AI model" task (2026-08-19)

Notes from an agent-driven session answering "how much does DoiT spend on tokens per AI model" using only the CLI (customer context `RSTDkHhaoGWwOEvlYlHyBUhm`). Each item is a candidate CLI or agent-guidance improvement.

> **Superseded by [FRICTION-SPEC.md](FRICTION-SPEC.md)** (2026-08-19), which audits each item against the code, corrects two errors in item 1 (the server honors `--max-results` up to 500 — 501+ silently resets to the default 50; and agent-mode TOON *does* carry `pageToken` — only csv/table drop it), and turns the list into a phased plan.

## CLI friction

1. **`list-dimensions` silently truncates to one API page.**
   `--max-rows 0` reads as "give me everything" but the command returned exactly one server page (50 items of 955). `--max-results 1000` didn't help either — the server caps the page size at 50 and the CLI didn't warn that the cap was applied. Worse, in `csv`/`table` output the `pageToken` is dropped, so there is **no signal at all** that 905 dimensions were missing. The agent only noticed because `service_description` was absent from a list it "knew" should contain it, then had to inspect the JSON wrapper and hand-write a 20-iteration `--page-token` loop in Python.
   *Suggestions:* (a) auto-paginate list commands in agent mode (or add `--all`/`--paginate`); (b) when more pages exist and the output format can't carry the token, print a stderr warning like `# 50 of 955 shown; use --page-token c2t1…`; (c) if the server clamps `--max-results`, say so.

2. **No practical way to search 955 dimensions for a concept.**
   The task hinged on discovering the `genai/*` system labels. `--filter` exists ("fields eligible: type, label, key") but its expression syntax is undocumented in `--help`, so the agent didn't risk it and grepped a locally assembled dump instead. A first-class `dci list-dimensions --filter 'label contains genai'` example in help text — or a `dci skill` note saying "dimension discovery: paginate all, grep locally" — would have saved four exploratory calls.

3. **Inconsistent null markers in query output.**
   The same result set renders missing group values as empty string in some rows and `[Value N/A]` in others (e.g. `cost_type` empty vs `token_type` `[Value N/A]`). Client-side classification code needs to treat both as null; pick one representation (empty for CSV, or a documented sentinel).

4. **No way to express "dimension is not null" in a query filter.**
   Grouping all billing data by `genai/*` labels produces a giant null-group row (~$418k of non-GenAI spend) that must be dropped client-side. `filters[].includeNull` only widens a value match; there's no `exists`/`not null` mode. A `mode: exists` (or documented `regexp: ".*"` recipe) would make label-scoped reports one query.

5. **Redundant time columns in flat CSV.**
   Each row carries `month`, `year`, and `timestamp` for the same period. Harmless but wasteful for token-limited agents; `timestamp` alone suffices in machine formats.

6. **Zero-cost group rows add noise.**
   Grouping by `genai/cost_type` emits `cost=0` rows for every model × `code_execution`/`web_search` combination (~40% of rows in one query). `--include-empty-rows` covers null-group rows only; consider also dropping all-zero non-null groups in agent mode, or a `--nonzero` flag.

## Data-side friction (not the CLI, but hurts every CLI consumer)

7. **`genai/model` values are not normalized across sources.**
   The same model appears as `Claude Opus 4.8` (Claude products telemetry), `claude-opus-4-8` (direct API), and `claude-4.6-opus-high-thinking` / `claude-opus-4-8-thinking-high` (Cursor, with reasoning-effort suffixes baked in). `genai/base_model` doesn't resolve it — it maps in *both* directions (title-case model → kebab id, kebab model → title-case name) and is empty for Cursor rows. Any per-model rollup requires hand-written normalization. A canonical `genai/base_model` (one spelling, populated for all sources) would make "spend per model" a single group-by.

8. **"Token spend" is encoded differently per provider.**
   Anthropic rows: `genai/cost_type = tokens`. OpenAI rows: `cost_type` empty, token nature only inferable from `genai/usage_type` (`Input`, `Output`, `…cache writes`). Cursor rows: neither, only `genai/billing_category` (`Usage-based` vs `Included in Business` vs `Errored, Not Charged`). A uniform `genai/cost_type` across providers would remove the per-provider classifier.

## What worked well (keep)

- `dci query` with stdin JSON + `--help-full` schema was enough to self-serve the whole analysis; no docs needed.
- `--output csv --max-rows 0` on `query` (unlike list commands) returned complete results.
- The `genai/*` system-label taxonomy is rich enough to answer model/product/token-type questions once discovered.
