# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

DCI CLI is a standalone CLI wrapper around [restish](https://github.com/rest-sh/restish) for the DCI API. It provides a `dci` command that works like `restish dci` but as a self-contained binary with auto-configuration.

## Build Commands

```bash
# Build the binary
go build -o dci

# Install globally
go install github.com/doitintl/dci-cli@latest

# Update restish dependency
go get -u github.com/rest-sh/restish
go mod tidy
```

## Architecture

The CLI is a single-file Go application (`main.go`) that wraps restish:

1. **Auto-configuration**: On first run, creates config at OS-specific paths (`~/Library/Application Support/dci/apis.json` on macOS, `~/.config/dci/apis.json` on Linux, `%APPDATA%\dci\apis.json` on Windows) with DCI API OAuth2 settings.

2. **Command rewriting**: Intercepts `os.Args` to prepend "dci" automatically, so users can run `./dci list-budgets` instead of `./dci dci list-budgets`.

3. **Customer context**: The `customer-context` subcommand manages a default `customerContext` query parameter stored in `customer_context` file alongside the config. This is auto-appended to all API calls.

4. **Custom table output**: Overrides restish's table formatter to handle DCI's list response format (objects with nested arrays like `{budgets: [...]}`).

5. **UI customization**: Hides restish's global flags and removes non-DCI commands (`api`, `completion`, generic commands) via `lockToDCI()`.

## Key Dependencies

- `github.com/rest-sh/restish` - Core REST client and CLI framework
- `github.com/spf13/cobra` - Command framework (via restish)
- `github.com/spf13/viper` - Configuration management (via restish)
- `github.com/alexeyco/simpletable` - Table rendering
