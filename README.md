# CloudCent CLI

![License](https://img.shields.io/badge/license-MIT-blue)
![Version](https://img.shields.io/badge/version-0.0.4--beta-orange)
![Integration Tests](https://github.com/OverloadBlitz/CloudCent-CLI/actions/workflows/integration-tests.yml/badge.svg)

![banner](/docs/cloudcent-banner.png)

- [Installation](#installation)
  - [npx](#run-instantly-with-npx)
  - [npm](#install-globally-with-npm)
  - [Shell script (macOS / Linux)](#shell-script-macos--linux)
  - [PowerShell (Windows)](#powershell-windows)
- [Quick Start](#quick-start)
- [Examples](#example)
  - [Draw.io](#drawio)
  - [Pulumi](#pulumi)
- [Supported Cloud Resources](#supported-cloud-resources)
- [CLI Commands](#cli-commands)
- [Configuration](#configuration)
- [Integration Tests](#integration-tests)
- [Contributing](#contributing)
- [Reporting Issues](#reporting-issues)
- [License](#license)

## Installation

### Run instantly with npx

```bash
npx @cloudcent/cli
```

### Install globally with npm

```bash
npm install -g @cloudcent/cli
```

### Shell script (macOS / Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/OverloadBlitz/cloudcent-cli/main/install.sh | bash
```

### PowerShell (Windows)

```powershell
irm https://raw.githubusercontent.com/OverloadBlitz/cloudcent-cli/main/install.ps1 | iex
```

## Quick Start

```bash
cloudcent --help
cloudcent init
```

Run `cloudcent init` to authenticate through your browser.

This will generate a free API key and store it at:

```text
~/.cloudcent/config.yaml
```

This API key is used to access the my pricing API.

No cloud or Pulumi account is required.

> **Note:** For Pulumi projects, only Python is currently supported.

## Example

### Draw.io
```
cloudcent diagram init aws-saas-example.drawio 

cloudcent diagram estimate aws-saas-example.drawio
```
TBD


### Pulumi
```
cloudcent pulumi estimate
```
![pulumi-demo](/docs/pulumi_demo.png)

## Supported Cloud Resources

| Provider | Services                                                      | Pricing Model | Data Source |
|----------|---------------------------------------------------------------|---------------|-------------|
| AWS | EC2, EBS, ECS, S3, ApiGateway, AppSync, DynamoDB, Lambda, SNS | OnDemand, Reserved, SavingPlan, Spot | AWS Pricing API |
| Azure | Virtual Machines                                                           | OnDemand, Reserved, SavingPlan (with/without Azure Hybrid Benefit) | Azure Pricing Calculator |
| GCP | WIP                                                           | OnDemand, CommittedUseDiscount, Preemptible | GCP Pricing SDK v1 |
| OCI | WIP                                                           | OnDemand (PAYG) | OCI Cost Estimator |

## CLI Commands

```
cloudcent                 # Show help
cloudcent init            # Authenticate via browser
cloudcent diagram init <file>      # Scaffold a YAML spec next to the diagram
cloudcent diagram estimate <file>  # Estimate costs from the diagram's spec
cloudcent pulumi estimate  # Estimate costs from pulumi codes
cloudcent history         # Show past queries
cloudcent cache stats     # Show cache statistics
cloudcent cache clear     # Clear cache and history
cloudcent metadata refresh  # Download latest pricing metadata
cloudcent config          # Show current configuration
```

## Configuration

Config is stored at `~/.cloudcent/config.yaml`

Data files:
- `~/.cloudcent/metadata.json.gz` — compressed pricing metadata
- `~/.cloudcent/cloudcent.db` — SQLite database (history, cache)


## Integration Tests
Currently, I am using the following repositories as integration test cases:

- [jgraph/drawio-diagrams](https://github.com/jgraph/drawio-diagrams)
- [pulumi/examples](https://github.com/pulumi/examples)

These test cases are located under:

```text
/integration_tests/testdata
```

Under the `/integration_tests/snapshots` directory, I store the expected total pricing results, which were manually adjusted based on the official cloud cost estimators.

The `run-tests.sh` script is for comparing the CLI output against the saved snapshots.

I am still working on adjusting and fixing test cases


## Contributing

1. Please create an issue first if you want to propose a change or fix.
2. I usually don’t create PRs in this repo. I use a separate private repo for creating, reviewing, and merging PRs, but feel free to open PRs here.
3. Feel free to use AI but need to pass integration tests before raising prs
4. Rebase against main branch and squash the commits before merging

## Reporting Issues

[Open an issue](https://github.com/OverloadBlitz/cloudcent-cli/issues)


## Honorable Mention
The `0.0.2-beta-legacy` branch includes a deprecated TUI for querying cloud costs across providers. It is no longer supported due to changes in the pricing data model, but remains noted here as an honorable mention.
This CLI also has a TUI mode, I just disabled it for now and am still working on it

## License

[MIT](LICENSE)
