# DCI CLI

A CLI for the DCI API - works exactly like `restish dci` but as a standalone `dci` command. This wrapper builds on Shay's work (#dci-cli), enabling DCI for restish.

## What This Does

This is a thin wrapper around [restish](https://github.com/rest-sh/restish) that:

- Auto-configures the DCI API on first run (`https://api.doit.com`)
- Makes all API commands available directly (no need for `restish dci`)
- Automatically prompts for OAuth2 authentication when needed
- Tracks the upstream restish library for updates
- Single binary - no external dependencies or install scripts

## Installation

```bash
# Go install (recommended)
go install github.com/doitintl/dci-cli@latest

# Or build locally
git clone https://github.com/doitintl/dci-cli.git
cd dci-cli
go build -o dci

# Run any command - authentication happens automatically on first use
./dci list-budgets
```

That's it! The binary auto-configures itself and prompts for authentication when needed.

## Usage

All API commands work directly without the `dci` prefix:

```bash
# These all work:
./dci list-budgets
./dci create-report
./dci query
./dci list-alerts
./dci validate
```

### See all available commands

```bash
./dci --help
```

This shows all API commands organized by category (Alerts, Budgets, Reports, etc.)

### Customer context defaults (Doers)

If you have access to multiple customer tenants, set a default `customerContext` once and it will be applied to every request (unless you override it with your own `-q` flags):

```bash
# Set / show / clear the default context
./dci customer-context set <TOKEN>
./dci customer-context show
./dci customer-context clear
```

The CLI automatically appends `-q customerContext=<TOKEN>` to your calls when a default is set.

Default output format is `table`. Override with `--output json`, `--output yaml`, or `--output auto`.

### Examples

```bash
# Get help for any command
./dci list-budgets --help

# Use filters and output formats
./dci list-budgets -f body.budgets --output json

# Table output (default)
./dci list-budgets

# Table options
# Wrap cells instead of truncating (or use -M wrap)
./dci list-budgets --output table --table-mode wrap
# Pick columns to include
./dci list-budgets --output table --table-columns id,name,amount

# Create resources
./dci create-budget name:"My Budget" amount:1000
```

## Updating

```bash
go get -u github.com/rest-sh/restish
go mod tidy
go build -o dci
```

## Configuration

Config file (per OS `user config` dir):

- macOS: `~/Library/Application Support/dci/apis.json` (legacy path still read)
- Linux: `$XDG_CONFIG_HOME/dci/apis.json` or `~/.config/dci/apis.json`
- Windows: `%APPDATA%\\dci\\apis.json`

Auto-created on first run. Delete it to reset.
