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
```

## Output Formats

The default output format is `table`. Override it with the `--output` flag:

```bash
dci list-budgets --output json
dci list-budgets --output yaml
dci list-budgets --output table
```

### Table Options

```bash
# Wrap long cell values instead of truncating
dci list-budgets --table-mode wrap

# Show only specific columns
dci list-budgets --table-columns id,name,amount
```

## Updating

```bash
# macOS (Homebrew)
brew update && brew upgrade dci

# Windows (WinGet)
winget upgrade DoiT.dci

# Windows (Scoop)
scoop update dci
```

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

## License

See [LICENSE](LICENSE) for details.
