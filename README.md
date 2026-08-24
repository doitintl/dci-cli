[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/doitintl/dci-cli)

# Cloud Intelligence™ CLI

`dci` is the command-line interface for the [Cloud Intelligence™](https://www.doit.com/) API. Manage budgets, reports, alerts, and run analytics queries directly from your terminal.

## Installation

```bash
# macOS (Homebrew)
brew install doitintl/dci-cli/dci

# Windows (WinGet)
winget install DoiT.dci

# Windows (Scoop)
scoop bucket add doitintl https://github.com/doitintl/dci-cli
scoop install dci

# Linux (.deb)
sudo dpkg -i dci_*_linux_amd64.deb

# Linux (.rpm)
sudo rpm -i dci_*_linux_amd64.rpm
```

Prebuilt binaries for all platforms are available on the [Releases](https://github.com/doitintl/dci-cli/releases/latest) page.

## Getting Started

On first run, `dci` automatically configures itself and opens a browser window for authentication via the DoiT Console. You can also sign in explicitly:

```bash
# Sign in to the DoiT Console
dci login

# Check your CLI configuration
dci status

# List your budgets
dci list-budgets

# List reports as a table
dci list-reports

# Run an analytics query from a Cloud Analytics report config
dci query < query.json
```

## Usage

```bash
# See all available commands
dci --help

# Get help for a specific command
dci list-budgets --help

# Get the machine-readable command catalog
dci commands --json
```

## AI Assistant

`dci ai` opens an interactive session where you can ask questions in plain English — the AI runs `dci` commands for you and explains the results — or run any command yourself with a `/` prefix:

```bash
dci ai
```

```text
› which of my budgets are at risk this month?
› /list-budgets --output table
› why is the second one over 90%?
```

Inside the session: type `/` to browse every command with completion, `↑/↓` for history, `Esc` to cancel a running command or answer, `/customer` to switch customer context, `/model` to pick the AI model, and `/help` for everything else. Commands you run yourself stay in the conversation, so follow-up questions can refer to what's on screen. Destructive commands the AI proposes always stop for your y/N confirmation first.

One-shot mode answers a single question and exits — handy in scripts:

```bash
dci ai "top 3 cost anomalies this month"
```

**Bring your own key.** AI features use your own Anthropic API key: export `ANTHROPIC_API_KEY`, or just ask a question in the session and it will walk you through saving one (stored locally with owner-only permissions). Your questions and the command results the AI reads are sent to Anthropic's API under your key; conversations are never stored anywhere but your terminal. Everything else — running `/` commands — works without a key.

Define your own shortcuts in `ai_commands.json` next to your dci config:

```json
{
  "top5": {"command": "list-reports --limit 5", "summary": "Top five reports"},
  "review": {"prompt": "Review last month's spend and flag anything unusual"}
}
```

## Resource Names

Commands that take a report, budget, or allocation accept the resource **name** as well as its ID — quoting is optional:

```bash
dci get-report "Monthly AWS Spend"
dci get-budget Team Backyard
dci open report Monthly AWS Spend
```

Matching is forgiving: exact name first, then case-insensitive, then a unique substring, then close typos. If several resources share the name, an interactive terminal lets you pick the right one from a list; scripts get the candidates with their IDs and an error so nothing runs on a guess. If nothing matches, the CLI points you at the right `list-*` command. Destructive commands such as `delete-report` always show the resolved name *and* ID before asking for confirmation, so a fuzzy match can never delete the wrong thing silently.

Running a command with **no argument at all** in an interactive terminal opens a filter-as-you-type picker over the same names — `dci get-report`, then type a few letters and press Enter. The same works for `dci open report`.

When you need precise control:

```bash
dci get-report --id <id>        # treat the argument as a literal ID, skip the lookup
dci get-report --name <value>   # force a name lookup, even if the value looks like an ID
DCI_NO_RESOLVE=1 dci ...        # turn name resolution off entirely (for scripts)
```

## Shell Completion

Tab completion for commands and flags is installed automatically by Homebrew
and the `.deb`/`.rpm` packages (bash, zsh, and fish). The release archives also
ship the scripts under `completions/`.

On Windows (WinGet or Scoop), add this line to your PowerShell profile:

```powershell
dci completion powershell | Out-String | Invoke-Expression
```

For any other setup, generate the script for your shell and load it from your
shell profile:

```bash
# bash (~/.bashrc)
source <(dci completion bash)

# zsh (~/.zshrc, after compinit)
source <(dci completion zsh)

# fish
dci completion fish > ~/.config/fish/completions/dci.fish
```

Run `dci completion <shell> --help` for shell-specific instructions.

Tab also completes resource names: `dci get-report Mon<TAB>` offers matching
report names, including multi-word ones. Type names bare when completing —
don't open a quote first. The shell escapes spaces for you
(`dci get-report Jack\ coach\ mark\ test`), whereas completion inside an
unclosed quote is not supported by shell completion frameworks. Names come
from a local cache that refreshes in the background: the very first Tab shows
a short "fetching names" notice while the cache warms — press Tab again a
moment later. Set `DCI_ACTIVE_HELP=0` to suppress such notices.

## Output Formats

The default output format is `table`. Override it with the `--output` flag:

```bash
dci list-budgets --output json
dci list-budgets --output yaml
dci list-budgets --output table
dci list-budgets --output toon
```

`toon` emits [Token-Oriented Object Notation](https://toonformat.dev/) — a
compact, lossless format that uses ~40% fewer tokens than JSON for list-shaped
data. It is opt-in and most useful when driving the CLI from an LLM agent.

### Table Options

```bash
# Wrap long cell values instead of truncating
dci list-budgets --table-mode wrap

# Full-screen scrollable viewer: ←→ scroll columns, s sort, / filter,
# Enter prints the selected row's id on exit (falls back to fit when piped)
dci list-anomalies --table-mode interactive

# Show only specific columns
dci list-budgets --table-columns id,name,amount

# Draw an ASCII graph of report period totals under the table
dci query <query.json --chart
```

### Interactive Terminal Features

In an interactive terminal the CLI adds a few conveniences on top of plain
tables — none of them affect scripts, pipes, CI, or agent mode:

- **Resource pickers** — `dci get-report` with no argument opens a
  filter-as-you-type list ([Resource Names](#resource-names)).
- **Query builder** — `dci query` with nothing piped walks you through
  composing a report config (time range, group-by dimensions, metric), shows
  the JSON, and can save it as a reusable `query.json`.
- **Confirmation prompts** — destructive commands show the resolved target in
  a confirmation dialog instead of requiring `--yes` (scripts still need
  `--yes` or `DCI_CONFIRM_DESTRUCTIVE=1`). Cancel is the default answer.
- **Budget utilization bars** and `--chart` graphs in table output.

Set `DCI_NO_TUI=1` to turn all of it off while keeping normal human-mode
tables and color.

### Timestamps and Timezones

Table output shows event timestamps — created, updated, acknowledged, and
similar moments — in your local timezone, with a one-line note on stderr
naming the zone. Everything else stays in UTC by design:

- **Report period columns** (daily/hourly cost buckets) and **anomaly usage
  windows** label UTC billing buckets; shifting them would move costs onto
  the wrong day.
- **Calendar dates** — contract terms, invoice dates, budget periods —
  render as plain dates.
- **Machine formats** (`json`, `yaml`, `csv`, `toon`) and agent mode always
  emit UTC or raw epoch values, so scripted output is identical on every
  machine.

```bash
# Keep table timestamps in UTC
dci list-budgets --utc

# Display in a specific timezone instead of the system one
DCI_TZ=Europe/Berlin dci list-budgets
```

## Agent Mode

`dci` adapts its output depending on whether a human or an AI agent is driving it.
In **human mode** (the default in an interactive terminal) you get tables, color,
and contextual hints. In **agent mode** the CLI emits clean, deterministic output
that is cheap to parse and free of decoration:

- Default `--output` becomes `toon` (compact, token-efficient) instead of `table`
- No color, spinners, or other terminal decoration
- Banners, tips, and status chatter go to **stderr**, leaving **stdout** for data only
- Explicit agent sessions emit machine-readable JSON errors with stable error codes, retry guidance, and distinct process exit codes
- `--fields` selects response fields and `--exclude` removes fields before output
- The request `User-Agent` carries a `mode=` token — `agent` (explicit flag/env or a known AI-agent environment), `noninteractive` (piped/redirected output or CI/CD), or `interactive` (human at a terminal) — so API traffic can be segmented by interface

### How agent mode is detected

Detection runs in priority order — the first match wins:

1. **`DCI_AGENT_MODE` environment variable** — `DCI_AGENT_MODE=1` forces agent mode,
   `DCI_AGENT_MODE=0` forces human mode. Always wins.
2. **`--agent` / `--no-agent` flags** — explicit, per-invocation override.
3. **Known agent environment variables** — if any of these are set, agent mode is
   assumed: `CLAUDECODE`, `CLAUDE_CODE`, `CURSOR_AGENT`, `KIRO_AGENT`,
   `AIDER_SESSION`, `GEMINI_CLI`, `REPLIT_AGENT`, `WINDSURF_AGENT`,
   `OPENHANDS_AGENT`, `DEVIN_AGENT`. (Open a PR to extend this list.)
4. **Non-TTY stdout** — a soft signal: when output is piped or redirected and no
   setting above applies, agent mode is assumed.

```bash
# Force agent mode
DCI_AGENT_MODE=1 dci list-budgets
dci --agent list-budgets

# Force human mode (tables, color) even when piping or running under an agent
dci --no-agent list-budgets | less -S
```

Run `dci status` to see whether agent mode is active and why.

Pass `--dry-run` to preview any API command. Most commands use a local preview and send no request; operations with an API-native `dryRun` parameter send a simulation request and return an action marked `"dry_run": true`. The CLI supplies an idempotency key when that simulation requires one. Commands classified as destructive require `--yes` or `DCI_CONFIRM_DESTRUCTIVE=1` before real execution in agent and non-interactive modes; an interactive terminal shows a confirmation prompt instead.

## Updating

Run `dci update` (alias: `dci upgrade`) to update in place on any OS. It knows how the binary was installed and does the right thing after a confirmation:

- **Homebrew / Scoop / WinGet** installs run the package manager's own upgrade command for you.
- **`.deb`/`.rpm`** installs download the new package, verify its checksum, and run the install step with `sudo` (you'll be asked for your password).
- **Standalone binaries** (tarball, `~/bin`, CI) are replaced directly after validating the release checksum.

Useful flags: `--check` only reports whether an update is available, `--yes` skips the confirmation, and `--version vX.Y.Z` pins or rolls back a standalone install to a specific release. In agent mode the command never installs without `--yes` and reports a JSON result.

The CLI also checks for new releases in the background at most once every few hours and prints a short notice when one is available; set `DCI_NO_UPDATE_CHECK=1` to turn that off.

> **Note (Homebrew 6):** `brew install` auto-trusts the formula, so upgrades normally just work. But `brew upgrade`/`brew reinstall` never trust the tap on their own — if `dci` upgrades fail or are silently skipped with an untrusted-tap message, run `brew trust doitintl/dci-cli` once to restore it.

## Authentication

By default, `dci` authenticates interactively via the DoiT Console (OAuth). For CI pipelines and non-interactive environments, set the `DCI_API_KEY` environment variable:

```bash
export DCI_API_KEY=<your-api-key>
dci list-budgets --output json
```

When `DCI_API_KEY` is set, the CLI skips the browser-based login and authenticates using the API key directly. Run `dci status` to verify the active auth method.

## Configuration

Configuration is stored in your OS user config directory:

| OS      | Path                                          |
| ------- | --------------------------------------------- |
| macOS   | `~/Library/Application Support/dci/apis.json` |
| Linux   | `~/.config/dci/apis.json`                     |
| Windows | `%APPDATA%\dci\apis.json`                     |

The config file is created automatically on first run. Delete it to reset to defaults.

`DCI_API_BASE_URL` overrides the API base for that invocation only — it is never written to the config file. To change the saved base permanently, edit `apis.json` directly.

## AI Agent Skill

This repo ships a reusable agent skill at `skills/dci-cli` that teaches AI coding agents how to operate the `dci` CLI safely and effectively.

Install it with the built-in subcommand:

```bash
dci skill claude   # installs to ~/.claude/skills/dci-cli/
dci skill codex    # installs to ~/.codex/skills/dci-cli/
dci skill kiro     # installs to ~/.kiro/skills/dci-cli/
dci skill gemini   # installs to ~/.gemini/skills/dci-cli/
```

Inspect or refresh installed skill files with:

```bash
dci skill list
dci skill update codex
dci skill update   # updates every detected installation
```

Installs and updates preserve locally edited managed files unless `--force` is passed. Forced overwrites save edited files in a uniquely named sibling backup directory first. Unmanaged files in the skill directory are left in place and do not block an update.

Run `dci skill --help` for the full list of supported agents.

Alternatively, for Codex you can use the `skill-installer` helper:

```bash
python3 ~/.codex/skills/.system/skill-installer/scripts/install-skill-from-github.py \
  --repo doitintl/dci-cli \
  --path skills/dci-cli
```

## License

See [LICENSE](LICENSE) for details.
