[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/doitintl/dci-cli)

# DoiT Cloud Intelligence CLI

`dci` is the command-line interface for the [DoiT Cloud Intelligence](https://www.doit.com/) API. Manage budgets, reports, alerts, and run analytics queries directly from your terminal.

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

# Run an analytics query
dci query body.query:"SELECT * FROM aws_cur_2_0 LIMIT 10"
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

# Show only specific columns
dci list-budgets --table-columns id,name,amount
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

Pass `--dry-run` to preview any API command. Most commands use a local preview and send no request; operations with an API-native `dryRun` parameter send a simulation request and return an action marked `"dry_run": true`. The CLI supplies an idempotency key when that simulation requires one. Commands classified as destructive require `--yes` or `DCI_CONFIRM_DESTRUCTIVE=1` before real execution.

## Updating

Run `dci upgrade` to check for a new version — it prints the upgrade command for your install method (it never installs anything itself). The CLI also checks for new releases in the background at most once every few hours and prints a short notice when one is available; set `DCI_NO_UPDATE_CHECK=1` to turn that off.

```bash
# macOS (Homebrew)
brew update && brew upgrade dci

# Windows (WinGet)
winget upgrade DoiT.dci

# Windows (Scoop)
scoop update dci
```

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
