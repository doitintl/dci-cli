# Design spec: `dci ai` — DoiT-provided Anthropic access for Doers (token vending)

Status: **maintainer-reviewed draft** (rev 4 — the maintainer's fetch-from-backend instinct adopted, with the credential upgraded: the console API performs the Anthropic WIF exchange **server-side** with its own GCP workload identity and vends short-lived tokens to the CLI. This supersedes rev 3's client-side federation and its DoiT-operated OIDC issuer, and rejects the fetch-a-static-key variant — see §4.5–§4.7. Decisions resolved by the maintainer on 2026-08-25 are recorded in §10.)
Audited at commit `3d130df`; every claim about existing code cites the function and file it is based on. Claims about Anthropic's federation product cite the official docs (references in §11) and the pinned SDK source, verified 2026-08-25.

Scope: giving Doers a keyless path into `dci ai`, using the login they already have — the Cloud Intelligence (DoiT Console) sign-in that every CLI user performs anyway. The CLI fetches a short-lived Anthropic access token from the console API; the console API obtains it via Anthropic Workload Identity Federation (WIF) using its own workload identity. Covers the credential design, the backend endpoint, the Anthropic Console setup, the CLI changes, and the security analysis. Doers first; the architecture deliberately extends to customers later (§4.5), if DoiT decides to offer provided-AI instead of bring-your-own-key.

**API keys remain a permanently supported, first-class path for everyone — Doers and non-Doers alike** (F12). Provided access is additive and lowest-precedence: an explicit `ANTHROPIC_API_KEY` or saved key always wins, and the guided key setup stays. Customers and partners are otherwise untouched: AI-SPEC D1 (bring-your-own-key) remains their default. Also out of scope: proxying model traffic through a DoiT service (rejected in AI-SPEC D1/D4 and not reopened here — the vended token changes *authentication only*: model traffic still flows directly from the user's machine to `api.anthropic.com`).

---

## 1. Summary

Today `dci ai` requires every user — including every Doer — to bring a personal Anthropic API key: `resolveAIKey` (ai_session.go) reads `ANTHROPIC_API_KEY` or `ai_settings.json`, and the TUI's guided setup (`renderAIKeyOnboarding`, ai_tui.go) walks new users through pasting one from the Anthropic Console.

For Doers this is the wrong trade. DoiT already operates a shared Anthropic organization, and its key hygiene is a known, actively-worked problem: an internal 2026-08 governance report found **dozens of active, never-used API keys** in the org — largely per-person keys named after their creators — and an internal CloudFlow key-governance initiative exists specifically to discover and deactivate stale Anthropic keys. (Specific key names and ticket references are deliberately kept out of this public document.) `dci ai`'s onboarding, as shipped, mints one more personal static key per Doer — perpetuating exactly the mess the org is paying to clean up. And the Doer population is not uniformly technical: any solution that assumes developer tooling excludes exactly the FinOps, sales, and CRE Doers the AI mode is for. The bar is **no new interaction at all** — and there is a login that already clears it.

Every `dci` user is *already signed in to Cloud Intelligence*: the CLI cannot function without `dci login` (`registerAuthCommands`, main.go), and the cached OAuth JWT already distinguishes Doers via the `DoitEmployee` claim (`cachedTokenIsDoer`, main.go). The design (F21): the CLI asks the console API for AI credentials; the console API checks the caller's entitlement (today: Doers) and returns a **short-lived, workspace-scoped, inference-only Anthropic access token** (`sk-ant-oat01-…`, ≤1 h, self-expiring). The backend obtains that token through Anthropic's **Workload Identity Federation** (GA) using its **own GCP workload identity** — the standard, documented GCP→Anthropic path (metadata-server identity token, exchanged at `/v1/oauth/token`). Two consequences worth stating plainly:

- **No static Anthropic credential exists anywhere in this system** — not on laptops, and not in Secret Manager either: the backend's credential *is* its Google-managed workload identity. There is nothing to rotate, store, or leak at rest.
- **DoiT does not become an OIDC issuer.** Rev 3's main cost — KMS signing keys, public JWKS, rotation discipline — disappears; the Anthropic-side setup is the Console wizard's standard **Google Cloud** tile pointed at the backend's service account.

The maintainer's simpler alternative — fetch a preconfigured static API key from Google Secret Manager — was evaluated and rejected (§4.6, F22): a fetched static key is a non-expiring shared org credential fanned out to every laptop, with no per-Doer containment, rotation that is Console-manual by platform design (the Admin API cannot *create* keys), and `workspace:developer`-equivalent power that cannot be scoped down to inference-only. The vended-token design keeps the exact same fetch-from-backend architecture and swaps only the credential class: minutes instead of forever, inference-only instead of everything.

AI-SPEC's decision log anticipated the direction: D8 (2026-08-24) recorded Anthropic WIF as viable for a doer identity bridge, deliberately reopening D1's billing posture for Doers only. This spec is the follow-through; rev 4 lands it in its minimal form. The trade accepted in choosing token vending over rev 3's per-user assertions: per-Doer identity no longer reaches Anthropic's authentication history — per-Doer visibility lives in DoiT's endpoint logs and the CLI's own token telemetry (§7.7), which is also where per-Doer *usage* was always going to come from, since Claude's Usage analytics attribute federated traffic at workspace level regardless (§7.7). Rev 3's DoiT-operated issuer with per-user assertions remains documented as the upgrade path (§4.7) if Anthropic's future `target_type=USER` rules ever make Anthropic-side per-user attribution worth the issuer duties.

Decision table (rationale in the cited sections):

| Decision | Choice | § |
|---|---|---|
| Architecture | Token vending: CLI fetches AI credentials from the console API; model traffic stays direct to `api.anthropic.com` (F21) | §4.5 |
| Backend credentialing | Anthropic WIF, server-side: the backend's GCP workload identity exchanged at `/v1/oauth/token` — standard GCP provider path | §4.5, §6 |
| Credential vended to the CLI | Short-lived `sk-ant-oat01-…` bearer token (≤1 h, self-expiring), never a static key (F22) | §4.5, §4.6 |
| Who gets it now | Doers only, gated server-side at the vend endpoint and client-side on `cachedTokenIsDoer()` (main.go) | §5.1 |
| Who could get it later | Customers/partners, per entitlement, through the same endpoint exchanging into separate rules/workspaces (F14) | §4.5 |
| API keys | Kept permanently, first-class, for Doers and non-Doers; always win precedence (F12) | §5.1 |
| Anthropic scope | `workspace:inference` — least privilege for a session that only calls the Messages API (F6) | §6.1 |
| Workspace | Dedicated `dci-cli-doers` workspace: isolated spend limits, rate limits, attribution (F7) | §6.1 |
| Endpoint owner | Console auth team (F15); callers: sessions + personal DCI API tokens (F18) | §4.5, §6.2 |
| Upgrade path | Rev 3's per-user-assertion issuer, if/when Anthropic-side per-user attribution earns it (F13, deferred) | §4.7 |

## 2. The problem, precisely

### 2.1 Current key plumbing

- `resolveAIKey` (ai_session.go): `ANTHROPIC_API_KEY` env wins over the `api_key` field of `ai_settings.json`. Empty → no session.
- `runAIOneShot` (ai_command.go) errors out with instructions to export the env var or edit the settings file.
- The interactive session opens keyless but degraded: `sessionNote` explains, and the first plain-text question triggers the guided key paste (`handleKeyEntryKey`, ai_tui.go), which validates shape (`aiValidateAPIKey`: `sk-` prefix) and persists to `ai_settings.json` (0600).
- The key is a **static org credential sitting in a dotfile on every laptop**, revoked only if someone notices.

### 2.2 Why Doers deserve a different answer than customers — today

AI-SPEC D1 deliberately puts the key and the agent loop on the user's machine — for *customers*, whose Anthropic relationship is their own. Doers are different in every relevant way:

- Their model usage should land on **DoiT's** Anthropic org, workspace-attributed, not on personal keys of unknowable provenance (the never-used-keys report shows where that leads). Notably, Doers have no self-serve alternative: an Anthropic API key requires Console-org membership with the Developer role, and a Claude Enterprise (claude.ai) seat grants no API access at all — the chat and API products are separate organizations.
- They already carry a strong, DoiT-controlled identity: the cached OAuth JWT with the `DoitEmployee` claim (`cachedTokenIsDoer`, main.go).
- Offboarding must revoke AI access. A pasted static key survives offboarding; a fetched short-lived token dies on its own within the hour.
- Many are **not CLI-native**. `dci`'s own onboarding already assumes only "can click through a browser login" (`dci login`, login_page.go); the AI credential must not raise that bar.

And one way they may *stop* being different tomorrow: if DoiT ever offers customers provided-AI (DoiT-billed model access as a Cloud Intelligence feature) instead of bring-your-own-key, the identity that gates it will be the same console login and the same vend endpoint (F14).

### 2.3 What good looks like

A Doer who has run `dci login` — which every `dci` user has, or the CLI does nothing — types `dci ai "why did acme's spend spike?"` and the answer streams. **No new interaction of any kind**: no Anthropic Console visit, no key paste, no env var, no second browser login, no developer tooling. Disabling their DoiT account (or their AI entitlement) turns the capability off within a token lifetime. Anyone who *prefers* a key — Doer or not — keeps using one, unchanged.

## 3. Anthropic Workload Identity Federation — research summary

Everything in this section is from the official docs (§11) and the pinned SDK source. Under rev 4 the WIF machinery runs **in the backend**, but its constraints still shape the design, so the summary stands.

### 3.1 Resource model

Three org-level resources, configured once in the Claude Console (**Settings → Workload identity**, "Connect workload" wizard) or via the Admin API:

- **Service account** (`svac_…`): a non-human principal in the Anthropic org. Minted tokens act as it; it joins workspaces like a user; rate limits and usage attribution follow the workspace.
- **Federation issuer** (`fdis_…`): registers an OIDC provider — the exact `iss` URL plus a JWKS source. For rev 4 this is simply Google (`https://accounts.google.com`, `discovery` mode): the backend's identity tokens are Google-signed, so DoiT registers no issuer of its own.
- **Federation rule** (`fdrl_…`): "JWTs from issuer X with claims like Y may act as service account Z with scope S." Match block: `audience` (exact), `subject_prefix`, `claims` (exact-match map, string-valued), and/or `condition` (CEL). On well-known shared issuers (**Google included**), Anthropic *requires* the rule to constrain tenant identity — for a GCP service account, pin both its numeric `sub` (uniqueId) and `email`. A single issuer can carry many rules — one per population or environment — which is what makes the customer extension (§4.5) a configuration exercise. Targets are **currently always a service account** — no per-human principal mapping yet (the SDK's option docs reference future `target_type=USER` rules; §4.7).

### 3.2 Token exchange

`POST https://api.anthropic.com/v1/oauth/token` with `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer`, the JWT as `assertion`, plus `federation_rule_id`, `organization_id`, `service_account_id`, and (when the rule spans multiple workspaces) `workspace_id`. Response: `access_token` (`sk-ant-oat01-…`), `token_type: Bearer`, `expires_in`, `scope`. The token is sent as a plain `Authorization: Bearer` header on API requests.

Constraints that shape this design:

- **Assertion requirements:** asymmetric signature with a `kid` in the issuer's JWKS; `sub`, `iat`, `exp` required; lifetime ≤ the issuer max (default 1 h). GCP metadata-server identity tokens satisfy all of this out of the box, with ~1 h validity.
- **Minted-token lifetime:** `max(60, min(rule.token_lifetime_seconds, 2 × remaining assertion validity))`; rule lifetime 60–86400 s, default 3600. The backend controls the effective vend lifetime via the rule.
- **Failure opacity:** every assertion denial is the same `401 authentication_error` / "Authentication failed"; the deny reason lands in the Console's **authentication history**. Under rev 4 only the backend ever sees these — the CLI sees DoiT-owned errors (§5.5).

### 3.3 SDK support (already in the tree)

`anthropic-sdk-go v1.66.0` — the version pinned in go.mod since the `dci ai` work — ships both halves:

- **For the CLI:** `option.WithAuthToken` (option/requestoption.go) sends a Bearer token — the vended `sk-ant-oat01-…` drops straight in. No new dependency, no federation code client-side.
- **For the backend (Go reference; the console API may differ):** `option.WithFederationTokenProvider` with a metadata-server identity-token provider implements the exchange-and-refresh loop; or the backend calls `/v1/oauth/token` directly — it is one HTTPS POST.
- Credential precedence with `ANTHROPIC_API_KEY` above everything else — a set (even empty) key env var wins. §5.1 mirrors this order deliberately.

## 4. The credential problem: what should reach the Doer's machine?

Rev 1–3 of this spec asked "where does a human's OIDC assertion come from?" and worked through six sources (§4.1–§4.4 keep the still-relevant verdicts). The maintainer's fetch-from-backend proposal reframed the question better: the CLI already has an authenticated channel to the console API, so **the backend can hand the CLI a credential directly** — the only real question is *which* credential. Two candidates:

| | Backend hands the CLI… | Lifetime | Scope | Per-Doer containment | Rotation | Verdict |
|---|---|---|---|---|---|---|
| G | **Short-lived WIF-minted token** (`sk-ant-oat01-…`) | ≤1 h, self-expiring | `workspace:inference` | entitlement cut stops new tokens; outstanding exposure ≤1 h | none needed — nothing static exists | **Primary** (§4.5, F21) |
| H | Static API key from Google Secret Manager | until manually rotated | `workspace:developer`-equivalent, not reducible | none — one shared key everywhere | Console-manual (Admin API cannot create keys) | **Rejected** (§4.6, F22) |

Earlier verdicts that still stand: forwarding the raw console token to Anthropic — rejected (credential reuse across trust domains; wrong audience); `gcloud` user identity tokens — rejected (audience confusion on gcloud's shared client ID); `gcloud` SA impersonation — an env-var escape hatch, no CLI code; browser OIDC logins against Cloudflare Access or a dedicated Google client — obsoleted by rev 4 (they existed to get a *client-side* assertion, which token vending no longer needs; rev 2 in PR history preserves the full designs).

### 4.5 Primary: token vending — the backend exchanges its own identity, the CLI gets a short-lived token (F21)

The console API gains one endpoint, owned by the **console auth team** (F15) — sketch; final path and response shape are theirs:

```
POST /auth/ai/token            (authenticated with the caller's console OAuth session
                                or a personal DCI API token — F18)
→ 200 { "access_token": "sk-ant-oat01-…", "expires_in": 1800 }
→ 403 { … }                    // caller not entitled (today: not a Doer)
```

Behind it, the backend — a GCP workload with ambient identity — runs the **standard documented GCP→Anthropic WIF path**: it requests a Google-signed identity token from the metadata server (`audience=https://api.anthropic.com`, `format=full`), exchanges it at `/v1/oauth/token` against a rule that pins the backend service account's `sub` + `email`, and returns the minted token. Because the minted token is not user-bound, the backend caches one token and serves it to all entitled callers until it nears expiry — the exchange runs a few times per hour org-wide, regardless of Doer count.

Why this is the right design:

1. **The login already happened.** `dci` is unusable without a Cloud Intelligence session; the AI credential rides the session the user necessarily has. Zero new interaction (§2.3). When the cached session is stale, the existing `dci login` flow is the recovery path — one the user already knows.
2. **No static Anthropic credential exists anywhere.** Not on laptops (the token self-expires), and not server-side either — the backend authenticates to Anthropic with its Google-managed workload identity, so there is no key in Secret Manager, nothing to rotate, and nothing whose leak outlives the hour. This is strictly stronger than both the static-key variant (§4.6) *and* rev 3 (which parked long-lived signing keys in KMS).
3. **No DoiT-operated issuer.** Rev 3's main cost — KMS key custody, public JWKS hosting, rotation discipline with a third-party dependent — disappears. Anthropic-side setup is the Console wizard's stock **Google Cloud** tile (§6.1); DoiT publishes nothing.
4. **The vend endpoint is the policy point DoiT already owns.** Who gets AI (per-population, per-entitlement, per-user kill switches, server-side rate limits, rollout percentages) is enforced where DoiT's account and tenant logic already lives. Revocation is DoiT-native: a disabled user or entitlement stops receiving tokens at once; outstanding exposure is bounded by the rule's `token_lifetime_seconds`.
5. **It extends to customers** (F14): the endpoint checks the caller's entitlement and — for a future customer population — exchanges into *different* rules and workspaces (per-population, per-tier, even per-tenant), all server-side, invisible to the CLI. The console login is the one identity every CLI user has; BYO-key remains the customer default regardless (F12).
6. **The CLI shrinks to almost nothing.** One authenticated fetch plus `option.WithAuthToken` — no OAuth flows, no federation options, no gcloud, no new persisted state (§5).

The trades, named: (a) per-Doer identity no longer reaches Anthropic — every exchange presents the backend's identity, so the Claude Console authentication history shows one workload; the per-Doer trail lives in DoiT's vend logs and the CLI's token telemetry (§7.7), which is where per-Doer *usage* numbers were coming from anyway (Anthropic's Usage analytics are workspace-level for federated traffic regardless). (b) The vend endpoint becomes a credential-issuing API and must be reviewed as one (§7.6). (c) A vended token is bearer-usable by any caller for its lifetime — mitigated by the short lifetime, inference-only scope, and workspace rate/spend limits (§7.2).

### 4.6 Rejected: vending a static API key from Secret Manager (F22)

The same architecture with the credential swapped for a preconfigured `sk-ant-api…` key stored in Google Secret Manager. Rejected on four grounds, one structural:

1. **No expiry means no revocation.** Cutting a Doer's entitlement stops future fetches, but every already-fetched copy works until the key itself is rotated. The result is one shared, hot, non-expiring org credential distributed to every Doer laptop — the never-used-keys pattern, automated.
2. **Rotation is manual by platform design.** The Admin API can list, rename, and disable API keys but **cannot create them** — key creation is Console-UI-only. The backend therefore cannot auto-rotate; every rotation is a human clicking in the Console and updating Secret Manager, so in practice rotations would be rare and leaks long-lived.
3. **Keys cannot be scoped down.** An API key carries the full non-administrative API surface of its workspace (`workspace:developer`-equivalent — Files, Skills, Managed Agents, …). WIF's `workspace:inference` (Messages-only) exists only for federated tokens.
4. **No per-Doer containment**: one laptop's leak is everyone's leak until that manual rotation.

The token-vending design (§4.5) keeps everything attractive about this variant — same endpoint, same entitlement check, same zero-interaction UX, arguably *less* total work (no Secret Manager, no rotation runbook) — and none of the four problems.

### 4.7 Deferred upgrade path: per-user assertions via a DoiT-operated issuer (F13)

Rev 3's design — the console API signs a per-user, audience-bound JWT (`sub`/`email`/`population` claims) with a KMS-held key and a published JWKS, and the *CLI* exchanges it — remains the documented upgrade if Anthropic-side per-user identity ever earns its cost: today it would put each Doer's email in the Console authentication history per exchange; if Anthropic ships `target_type=USER` rules (F20: deliberately not chasing the roadmap), it becomes true per-user principals. The vend endpoint's contract makes the upgrade invisible to users: same endpoint, same entitlement, different credential mechanics behind it. Until then, the issuer duties (key custody, JWKS, rotation discipline) buy nothing that DoiT's own vend logs don't already provide.

### 4.8 CI and agents, for free

The same Anthropic workspace extends to non-human contexts with one extra rule each, no CLI change: a **GitHub Actions** rule (`iss: https://token.actions.githubusercontent.com`, subject pinned to `repo:doitintl/dci-cli:…`) lets the FinOps eval harness (eval/run-finops.sh, which today needs a raw `ANTHROPIC_API_KEY` in CI secrets — see PR #100's friction note) run keyless via the SDK's standard federation env vars — AI-SPEC D8(a) verbatim. The governance report's stale CI keys are the same pattern, retired.

## 5. CLI design

### 5.1 Credential resolution (F1, F12)

`resolveAIKey` grows into `resolveAICredentials` (new chapter file `ai_credentials.go`, per the AGENTS.md chapter-split convention), returning a mode + the SDK options to construct the client with:

1. `ANTHROPIC_API_KEY` env (trimmed, non-empty) → API key mode. Unchanged, works for everyone, forever.
2. `ai_settings.json` `api_key` → API key mode. Unchanged, works for everyone, forever — including Doers who prefer a key; the guided key paste stays available to them.
3. `cachedTokenIsDoer()` **and** provided access not disabled (§5.2) → **provided mode**: fetch a token from the vend endpoint (§4.5). The server-side entitlement check is authoritative; the client-side doer gate only avoids a pointless round trip. (If F14 ever extends provided access to customers, this gate widens to "vend endpoint says yes" — the resolution chain itself doesn't change.)
4. Otherwise → no credentials; existing guided setup.

An explicit key always beating provided access mirrors the Anthropic SDK's own precedence (where `ANTHROPIC_API_KEY` shadows every other source) and keeps two properties: a user the endpoint doesn't entitle can never enter the provided path, and anyone who needs a personal key (evals, a different org) just exports or saves one — no new opt-out concept.

Non-doer behavior is byte-for-byte unchanged today.

### 5.2 Configuration

The provided path needs no baked-in identifiers at all — the vend endpoint is on the API base the CLI already knows (`apiBase()`, main.go), and everything Anthropic-related lives server-side. Configuration surface: `DCI_AI_PROVIDED=off` (env or `ai_settings.json`) disables the path entirely; `DCI_AI_PROVIDED_URL` overrides the endpoint for staging. The SDK's own `ANTHROPIC_*` federation env vars remain functional for power users (§4.4's escape hatch), but the CLI does not document them as its interface; `dci`'s contract is the `DCI_*` surface.

### 5.3 Client construction

`newLocalAISession` (ai_session.go) currently takes `apiKey string` and builds `anthropic.NewClient(option.WithAPIKey(apiKey))`. It changes to take the resolved credentials and pass through their `[]option.RequestOption`:

- API key mode → `option.WithAPIKey(key)`, as today.
- Provided mode → `option.WithAuthToken(token)` with the vended bearer token.

The CLI tracks the vend response's `expires_in` and re-fetches shortly before expiry (sessions are long-lived; turns are not — the natural seam is a pre-turn freshness check in the session loop, rebuilding the client's auth option when the token has rolled). A vend `401` (stale console session) surfaces as the standard re-login prompt the user already knows; a `403` means not entitled (§5.5). The fetch is a function value, so the loop is testable with scripted tokens — the same seam pattern as `aiModelStreamer` (ai_session.go). The token lives in process memory only; nothing new is persisted under the config dir.

### 5.4 Onboarding and status surfaces

- **TUI session:** a keyless, entitled Doer never sees credential UI at all — the first question just works. The key paste (`renderAIKeyOnboarding`) remains the path for non-doers and for anyone whose vend attempt 403s, with one added line for doers explaining both options. The model line (`ai_tui.go` — today "API key from env" / "API key from ai_settings.json") gains a third source: `DoiT-provided access`.
- **One-shot:** `runAIOneShot` works identically — the vend call is non-interactive, so pipes and CI behave the same as a TTY. It authenticates with the console session or, in scripted contexts, a personal DCI API token (`DCI_API_KEY`) — the endpoint accepts both (F18), so Doer scripts get provided access with no extra setup.
- **`dci ai` help text** (registerAICommand): the "yours" framing of the key requirement gets a doer carve-out, and the data-flow sentence is updated — Doer traffic runs under DoiT's organization (§7.4).

### 5.5 Errors

Two new failure classes map into the existing `aiFriendlyAPIError` (ai_command.go) path:

- **Vend refused:** `401` → the standard console re-login guidance; `403` → "your account isn't enabled for built-in AI access" plus the key-based alternative. Both are DoiT-owned errors, so the endpoint's message can be surfaced verbatim. Anthropic's opaque exchange failures (§3.2) are the backend's to diagnose — the CLI never sees them, a debuggability *improvement* over every client-side federation variant.
- **Model-call 401 mid-session** (token expired despite the freshness check, or revoked): re-fetch once and retry the turn; if the re-fetch 403s, fall through to the vend-refused handling.

Both fail the *turn* with a note, not the session — `/` commands keep working, matching the existing keyless-session posture (AI-SPEC §2).

## 6. Setup runbook

### 6.1 Anthropic Console, one-time (org admin)

1. **Workspace** `dci-cli-doers` (F7): isolates rate limits and spend; per-workspace usage reporting gives cost attribution for the Doer population and aligns with the internal key-governance initiative's workspace-per-group direction. Set a spend limit from day one — the number is set with FinOps (F19).
2. **Connect workload wizard, standard Google Cloud tile:** issuer `https://accounts.google.com` (`discovery` — the wizard prefills it); **Anthropic** service account `dci-ai-doers` (`svac_…`), added as a member of the `dci-cli-doers` workspace; rule `dci-ai-vendor`: `audience: https://api.anthropic.com`, `claims.sub` = the backend GCP service account's numeric uniqueId and `claims.email` = its address (both pinned, per Anthropic's GCP guide — never `subject_prefix` with Google), scope **`workspace:inference`** (F6 — the session makes only Messages streaming calls today, via `newAnthropicStreamer`, ai_session.go; the scope additionally covers token counting and Models, headroom the CLI may use later, while excluding Files/Skills/Managed Agents that `workspace:developer` would grant for nothing), `token_lifetime_seconds: 1800` (revocation lag bound; tunable 60–86400), workspace `dci-cli-doers`.
3. Verify with the wizard's 15-minute test window: one exchange from the backend (or a spike script running as its service account), confirm `sk-ant-oat01-…`.

### 6.2 Cloud Intelligence side, one-time (console auth team, F15)

- The vend endpoint per §4.5: authenticate the caller with the existing session machinery or a personal DCI API token (F18 — service accounts excluded at launch), enforce the Doer entitlement, exchange the backend's metadata-server identity token at `/v1/oauth/token`, cache the minted token until near expiry, return it with `expires_in`.
- Attach a dedicated, otherwise-unprivileged GCP service account to the backend for this exchange (not the compute default SA), so the Anthropic rule pins an identity whose only job is this.
- Log every vend (caller, `sub`, population) — this is the per-user audit trail that complements the workspace-level view at Anthropic.

### 6.3 Kill switches and lifecycle

- **Org-wide off, DoiT side:** disable the vend endpoint (feature flag) — new tokens stop immediately, everywhere, without touching Anthropic.
- **Org-wide off, Anthropic side:** archive the rule — exchange stops immediately; outstanding tokens die within `token_lifetime_seconds`.
- **Per-user off:** disable the user or their entitlement in Cloud Intelligence. Worst-case lag: the remaining life of the token they last fetched (≤30 min at the §6.1 setting).
- **Rotation:** nothing to rotate, anywhere. Google rotates its signing keys; Anthropic's discovery-mode JWKS cache follows; DoiT holds no static Anthropic credential and no signing keys.

## 7. Security and privacy

Extends AI-SPEC §11; nothing there is weakened.

1. **No static Anthropic credential exists in the system.** Not on Doer laptops (`ai_settings.json` stops accumulating org keys; the vended token lives in process memory and self-expires), not in Secret Manager, not in KMS — the backend authenticates to Anthropic with its Google-managed workload identity. The only local secret remains the console token cache the CLI has always had.
2. **Blast radius of a leaked credential:** a leaked vended token is ≤30 min (rule-configurable), inference-only, one rate-limited and spend-capped workspace. Compare the personal key it replaces: revoke-on-notice, workspace-developer surface. The console token's exposure is unchanged from today — and it is never sent to Anthropic.
3. **The trust chain is explicit:** Cloud Intelligence login (existing) → vend endpoint (DoiT entitlement policy, per-user, logged) → backend's Google-signed workload identity → Anthropic rule (issuer + audience + pinned `sub`/`email`) → workspace-scoped inference token. Every link is independently revocable and logged (DoiT vend logs; Anthropic authentication history for the backend's exchanges).
4. **Data flow improves.** Under a personal key, a Doer's question data and customer cost data flowed to Anthropic under that individual's personal agreement — AI-SPEC §11 flags exactly this. Provided, the traffic runs under **DoiT's organization and terms**. The loop stays on the user's machine; D1's architecture is untouched — DoiT vends credentials, never touches model traffic.
5. **Prompt/model isolation is unchanged:** credentials still never enter model context — the executor env-passing posture of AI-SPEC §11 applies to vended tokens identically.
6. **New obligations, named:** (a) the vend endpoint is a credential-issuing API and must be reviewed as one (authn, entitlement, rate limits, logging, abuse monitoring); (b) a vended token is bearer-usable by anyone holding it for its lifetime — the short lifetime, inference scope, and workspace limits are the containment; (c) Anthropic-side identity is the backend's, not the Doer's — per-user visibility lives in DoiT's vend logs (and §7.7's telemetry), with §4.7 as the upgrade path if that ever needs to move server-of-record to Anthropic.
7. **Per-Doer usage analytics do not come from Anthropic — by design of today's WIF.** Claude's Usage & Cost API groups only by `api_key_id`/`workspace_id`/`model`/`service_tier`/`context_window`/`inference_geo`/`speed`; federated tokens are not API keys, so all Doers appear as one workspace-level line (`dci-cli-doers`). The exact per-Doer numbers come from the CLI itself: `aiTurnDone` already carries per-turn input/output/cache-read token counts (surfaced by `aiStatsLine`, ai_command.go), and the vend endpoint knows the user — a lightweight CLI→console usage report (P2) yields per-Doer tokens, from which cost is deterministic. This also replaces DoiT's own key-creator-matching heuristic for AI-spend attribution, which a keyless design would otherwise blind for this traffic.

## 8. Phases

- **P0 — spike (no product code):** run §6.1 end to end using the backend's (or a scratch) GCP service account: metadata/impersonated identity token → exchange → one `dci ai` turn against the minted token via `ANTHROPIC_AUTH_TOKEN`. Confirms the rule config, token lifetimes, and that a `sk-ant-oat01-…` bearer drives the existing session untouched. Exit: a green checklist attached to this spec's PR. (Materially smaller than rev 3's spike — no scratch JWKS or hand-signed JWTs.)
- **P1 — launch, default-on for all Doers (F17):** the vend endpoint (Doer entitlement; sessions + personal API tokens, F18) and `ai_credentials.go`, **enabled for every Doer from the first release** — no staged pilot cohort, per maintainer decision: the endpoint's entitlement check and the workspace spend limit (set with FinOps before launch, F19) are the safety net, and `DCI_AI_PROVIDED=off` plus the untouched key path are the opt-outs. Doer onboarding stops leading with the key paste; README/help updates; announce internally; watch vend logs and workspace spend closely in the first weeks. Release note: user-facing for Doers → `feat:`, patch bump per AGENTS.md versioning policy.
- **P2 — later:** per-Doer usage reporting from the CLI to the console API (§7.7); CI rule for the eval harness (§4.8); revisit USER rule targets when Anthropic ships or announces them (F20), which would activate the §4.7 upgrade path; and — only if the product decision is made (F14) — the customer extension: entitlement logic at the vend endpoint, per-population rules and workspaces.

## 9. Alternatives considered and rejected or demoted

- **Vending a static Secret Manager key** (§4.6, F22): no expiry, Console-manual rotation, un-scopable, no per-Doer containment.
- **Forwarding the raw console token to Anthropic**: credential reuse across trust domains; wrong audience.
- **`gcloud` user identity tokens**: audience confusion on gcloud's shared client ID.
- **`gcloud` SA impersonation**: sound but gcloud-bound; kept as a no-code escape hatch via the SDK's federation env vars.
- **Browser OIDC login (Cloudflare Access SaaS / dedicated Google client)** — rev 2's primary: existed to obtain a *client-side* assertion, which token vending no longer needs; adds a login hop and an at-rest refresh token for nothing. Full designs preserved in PR history.
- **Client-side federation with a DoiT-operated issuer** — rev 3's primary: now the §4.7 deferred upgrade; its issuer duties buy only Anthropic-side per-user identity, which nothing requires yet.
- **A DoiT-hosted model proxy** (users hit a DoiT service that holds one credential and relays *model traffic*): re-litigates AI-SPEC D1/D4 — server in the token-streaming path, transcript custody, availability coupling. Note the distinction rev 4 preserves: DoiT is in the *credential* path (one small fetch per half hour), never the model-traffic path.
- **Inviting Doers to the Anthropic Console org** (self-serve keys or `ant auth login`): requires per-Doer org membership with the Developer role — which mints unlimited org-billed keys per member, keys that outlive their creators; a Claude Enterprise (claude.ai) seat, conversely, grants no API access at all. Heavier onboarding and worse governance than the thing being replaced.

## 10. Decisions resolved with the maintainer (2026-08-25)

- **Architecture (rev 4):** fetch-from-backend adopted; credential = **short-lived WIF-vended token**, never a static key (F21, F22). Supersedes rev 3's client-side federation; the DoiT-operated issuer becomes the deferred §4.7 upgrade path.
- **Q1 — endpoint owner:** the **console auth team** (F15). Callers: interactive sessions **and personal DCI API tokens**; service accounts excluded at launch (F18).
- **Q2 — rollout:** **all Doers from P1**, no staged pilot cohort — the entitlement check and workspace spend limit are the safety net (F17).
- **Q3 — spend limit:** the number is **set with FinOps**; the spec mandates only that a limit exists from day one (F19).
- **Q4 — USER targets:** **wait** — no roadmap ping to Anthropic; revisit when the feature ships or is announced (F20). Adopting it later activates §4.7.
- **Q5 — issuer identity:** rev 3's decision (console.doit.com JWKS) is **obsoleted by rev 4** — DoiT registers no issuer; the backend federates under Google's (F16 superseded by F21).
- **Q6 — customer extension:** stays **architected-for, not built** (F14); a dedicated spec is written only when the product/commercial decision is made.

Remaining open (intentionally): the concrete spend number and alert thresholds (with FinOps, pre-launch), and the final endpoint path/response shape (console auth team's).

## 11. References

- Workload Identity Federation (overview): https://platform.claude.com/docs/en/manage-claude/workload-identity-federation
- WIF reference (env vars, precedence, validation, errors): https://platform.claude.com/docs/en/manage-claude/wif-reference
- GCP provider guide (the backend's exact path): https://platform.claude.com/docs/en/manage-claude/wif-providers/gcp
- Federation rules Admin API (multi-rule issuers, per-workspace enablement): https://platform.claude.com/docs/en/api/admin/federation_rules
- Admin API (API keys: list/update only — creation is Console-only): https://platform.claude.com/docs/en/manage-claude/admin-api
- Usage & Cost API (grouping dimensions; workspace-level attribution): https://platform.claude.com/docs/en/manage-claude/usage-cost-api
- GA announcement: https://claude.com/blog/workload-identity-federation
- Pinned SDK surfaces: `anthropic-sdk-go v1.66.0`, `option/requestoption.go` (`WithAuthToken`; `WithFederationTokenProvider` for the backend reference)

## 12. Decision log

| # | Decision | Rationale |
|---|---|---|
| F1 | Provided access is entitlement-gated (today: Doers) and lowest-precedence | Non-entitled users can't enter the path; explicit key always wins, mirroring the SDK's own precedence; no new opt-out concept |
| F2 | gcloud SA impersonation is a documented escape hatch via SDK env vars, not CLI code | Technically sound but assumes gcloud; anyone who can run it can export four env vars |
| F3 | *(superseded by F13, then F21)* | The mint-endpoint idea evolved: rev 3 promoted it as a client-side assertion issuer; rev 4 lands it as server-side token vending |
| F4 | Reject raw console token as assertion | Live DCI credential handed to a third-party endpoint; audience structurally wrong |
| F5 | Reject gcloud user identity tokens | User creds can't set audience; shared gcloud client ID = org-wide replay surface |
| F6 | Scope `workspace:inference` | Least privilege: the session only calls the Messages API (`newAnthropicStreamer`, ai_session.go) |
| F7 | Dedicated `dci-cli-doers` workspace | Spend limits, rate limits, and attribution isolated; future populations get their own workspaces, never this one |
| F8 | *(revised again)* No federation constants anywhere in the binary | Rev 4 moves all Anthropic parameters server-side; the CLI knows only the vend endpoint on its existing API base |
| F9 | Anthropic SDK surfaces already pinned suffice | `WithAuthToken` for the CLI; `WithFederationTokenProvider` as the backend's Go reference; no dependency change |
| F10 | *(obsoleted by F21)* Browser OIDC login against Cloudflare Access for SaaS | Existed to obtain a client-side assertion; token vending removes the need. Full design in PR history (rev 2) |
| F11 | *(folded into F10, obsoleted with it)* | — |
| F12 | API-key auth stays permanently, for Doers and non-Doers | Maintainer requirement; keys remain first-class (env, settings, guided paste) and always win precedence |
| F13 | *(deferred to the §4.7 upgrade path)* DoiT-operated issuer with per-user assertions | Its issuer duties (KMS, JWKS, rotation) buy only Anthropic-side per-user identity; activate if/when USER targets (F20) make that worthwhile |
| F14 | Customer extension is architected for, not built | Provided-AI for customers is a product/commercial decision; when made, it is entitlement logic + per-population rules/workspaces behind the same vend endpoint. BYO key stays the customer default (AI-SPEC D1) |
| F15 | The vend endpoint is owned by the **console auth team** (2026-08-25) | It is an identity/credential concern, adjacent to the existing OAuth token endpoint |
| F16 | *(superseded by F21)* Issuer identity console.doit.com, JWKS-only | Rev 4 registers no DoiT issuer — the backend federates under Google's issuer via the standard GCP tile |
| F17 | Default-on for **all Doers at P1** — no staged pilot (2026-08-25) | The endpoint's entitlement check and the workspace spend limit are the safety net; `DCI_AI_PROVIDED=off` and the key path are the opt-outs |
| F18 | Vend callers: interactive sessions **and personal DCI API tokens**; service accounts excluded at launch (2026-08-25) | One consistent story for interactive and scripted Doer usage from day one; the non-human migration stays out of scope |
| F19 | The workspace spend number is set **with FinOps**; the spec mandates only that a limit exists from day one (2026-08-25) | Budget ownership sits with whoever owns the Anthropic org spend, not with this spec |
| F20 | USER rule targets: wait, don't ping Anthropic's roadmap now (2026-08-25) | No dependency signal on an unshipped feature; adopting it later activates the §4.7 upgrade path |
| F21 | **Token vending** (2026-08-25, maintainer): the backend exchanges its own GCP workload identity via WIF server-side and vends short-lived inference-scoped tokens to the CLI | Keeps the maintainer's fetch-from-backend architecture; no static Anthropic credential anywhere; no DoiT issuer; smallest CLI; extends to customers. Trade accepted: Anthropic-side identity is the backend's, per-Doer visibility stays in DoiT logs |
| F22 | **Reject vending a static Secret Manager key** (2026-08-25) | Non-expiring shared credential on every laptop; rotation is Console-manual (Admin API cannot create keys); `workspace:developer`-equivalent scope cannot be reduced; no per-Doer containment |
