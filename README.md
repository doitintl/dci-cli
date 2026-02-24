# DoiT Cloud Intelligence CLI

`dci` is the command-line interface for the DoiT Cloud Intelligence API.
It is built on top of [restish](https://github.com/rest-sh/restish), with DCI preconfigured and optimized for DoiT workflows.

## What This Does

- Auto-configures the DCI API on first run (`https://api.doit.com`)
- Uses OAuth2 with the DoiT console (`https://console.doit.com`)
- Lets you run DCI API operations directly as `dci <command>`
- Defaults to body-focused table output for day-to-day FinOps usage
- Tracks upstream restish improvements

## Installation

```bash
# Go install (recommended)
go install github.com/doitintl/dci-cli@latest

# Or build locally
git clone https://github.com/doitintl/dci-cli.git
cd dci-cli
go build -o dci
```

## Quickstart

```bash
# See local DoiT CLI context
./dci status

# Common DCI workflows
./dci list-budgets
./dci list-reports --output table
./dci query body.query:"SELECT * FROM aws_cur_2_0 LIMIT 10"
```

`./dci status` shows local CLI configuration and default context.

## Usage

```bash
# Show all available DCI API commands
./dci --help

# Show details for a specific command
./dci list-budgets --help
```

Help commands are local and do not trigger OAuth:

```bash
./dci
./dci --help
./dci help
```

All API commands work directly without `restish dci`:

```bash
./dci list-alerts
./dci create-report
./dci validate
```

## Output

Default output format is `table`.

You can override with:

- `--output table`
- `--output json`
- `--output yaml`
- `--output auto`

Table options:

```bash
# Wrap cells instead of truncating
./dci list-budgets --output table --table-mode wrap

# Pick columns
./dci list-budgets --output table --table-columns id,name,amount
```

## Updating

```bash
go get -u github.com/rest-sh/restish
go mod tidy
go build -o dci
```

## Development

```bash
# Keep the binary name stable for local use
go build -o dci .

# Quality checks
go test ./...
go vet ./...
```

CI runs these checks automatically on pull requests and pushes to `main`.

## Configuration

Config file (per OS user config directory):

- macOS: `~/Library/Application Support/dci/apis.json` (legacy path still read)
- Linux: `$XDG_CONFIG_HOME/dci/apis.json` or `~/.config/dci/apis.json`
- Windows: `%APPDATA%\\dci\\apis.json`

Auto-created on first run. Delete it to reset.
The CLI currently uses a single fixed profile (`default`).
Profile override flags (`-p`, `--profile`, `--rsh-profile`) are intentionally disabled.

## Internal Commands

`customer-context` is intentionally hidden from regular help and reserved for internal Doer workflows.
