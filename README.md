# CloudCent CLI

![License](https://img.shields.io/badge/license-MIT-blue)
![Version](https://img.shields.io/badge/version-0.0.5--beta-orange)
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
- [CI/CD Cost Guardrail](#ci/cd-cost-guardrail)
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
cloudcent pulumi estimate  # Estimate costs from pulumi codes (see --budget/--baseline guardrails)
cloudcent history         # Show past queries
cloudcent cache stats     # Show cache statistics
cloudcent cache clear     # Clear cache and history
cloudcent metadata refresh  # Download latest pricing metadata
cloudcent config          # Show current configuration
```

## CI/CD Cost Guardrail

`cloudcent` can gate pull requests on cloud cost — block (or just warn) when an
IaC change pushes the estimate over budget or adds too much versus the base
branch. The judgment runs natively in the CLI, so it behaves identically locally,
in GitHub Actions, or in any other CI.

### Guardrail flags

`cloudcent pulumi estimate` accepts:

| Flag | Effect |
| --- | --- |
| `--budget <usd>` | Fail if monthly total exceeds this absolute budget. |
| `--baseline <path>` | A previous estimate JSON (e.g. the base branch) to diff against. |
| `--max-increase <usd>` | Fail if the increase over `--baseline` exceeds this amount. |
| `--max-increase-pct <pct>` | Fail if the increase over `--baseline` exceeds this percentage. |
| `--no-fail` | Report breaches but always exit 0 (warn-only). |

**Exit codes:** `0` = pass · `1` = runtime error (build/auth/network) · `2` = a
guardrail threshold was breached. When `-o json` is used, the verdict is also
embedded under a top-level `"guardrail"` key.

```bash
# Absolute budget
cloudcent pulumi estimate ./infra --budget 500

# Diff against the base branch
cloudcent pulumi estimate ./infra -o json > base.json   # on the base revision
cloudcent pulumi estimate ./infra --baseline base.json --max-increase 50 --max-increase-pct 20
```

### GitHub Action

A reusable composite action lives at the repo root. It installs the CLI,
downloads pricing metadata, estimates the base branch via a `git worktree` diff,
evaluates the guardrail, and posts a sticky cost comment on the PR:

```yaml
permissions:
  contents: read
  pull-requests: write
steps:
  - uses: actions/checkout@v4
    with: { fetch-depth: 0 }          # needed for the base-branch diff
  - uses: CloudCentIO/cost-estimator-cli-rs@v1
    with:
      project-path: infra
      budget: "500"
      max-increase: "50"
      max-increase-pct: "20"
    env:
      CLOUDCENT_API_KEY: ${{ secrets.CLOUDCENT_API_KEY }}
      CLOUDCENT_CLI_ID: ${{ secrets.CLOUDCENT_CLI_ID }}
```

Required repository secrets: `CLOUDCENT_API_KEY` and `CLOUDCENT_CLI_ID`.

Ready-to-copy workflows are in [`examples/workflows/`](examples/workflows):
- [`cost-guardrail.yml`](examples/workflows/cost-guardrail.yml) — uses the composite action.
- [`cost-guardrail-raw.yml`](examples/workflows/cost-guardrail-raw.yml) — installs the CLI and runs the steps manually.

If the base branch has no Pulumi project (e.g. the PR introduces the infra), the
diff is skipped automatically and only the absolute `--budget` check applies.


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
