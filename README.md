# Budget Daemon

A personal finance automation daemon written in Go. Syncs transactions from bank accounts via Plaid, categorizes them with AI, evaluates budget rules, sends email alerts, and generates monthly spending reports.

## Features

- **Multi-account sync** — Connects to banks via Plaid (Discover, CNB, Fidelity, etc.) with CSV import fallback
- **3-tier AI categorization** — User overrides > keyword matching > Claude API batching. ~70% of transactions never hit the API
- **Budget rules & alerts** — Set per-category or total spending limits with threshold alerts (80%, 100%) via email
- **Monthly reports** — Plain text or HTML reports with category breakdowns, top merchants, budget performance bars, and month-over-month deltas
- **AI spending analysis** — Claude-powered anomaly detection and savings suggestions, with personal context support
- **Single binary** — No dependencies at runtime. Cross-compiles to Raspberry Pi with one command

## Quick Start

### Prerequisites

- Go 1.22+
- [Plaid account](https://dashboard.plaid.com) (production access for real banks)
- [Claude API key](https://console.anthropic.com)
- [SendGrid account](https://signup.sendgrid.com) (free tier, for email alerts)

### Install

```bash
git clone git@github.com:DylanCoon99/budget-daemon.git
cd budget-daemon
go build -o budget ./cmd/budget/
sudo cp budget /usr/local/bin/budget
```

### Configure

```bash
mkdir -p ~/.budget
cp .env.example ~/.budget/.env
chmod 600 ~/.budget/.env
```

Edit `~/.budget/.env` with your API keys:

```
ENCRYPTION_KEY=<run: openssl rand -hex 32>
PLAID_CLIENT_ID=<from Plaid dashboard>
PLAID_SECRET=<from Plaid dashboard>
PLAID_ENV=production
CLAUDE_API_KEY=sk-ant-...
SENDGRID_API_KEY=SG....
NOTIFY_EMAIL=you@example.com
```

### Connect Accounts

```bash
# Connect via Plaid (opens browser for bank login)
budget setup plaid "Discover"
budget setup plaid "CNB Bank"

# Or register for CSV import
budget setup csv "My Bank" "Checking" --type checking

# Check connections
budget setup status
```

### Usage

```bash
# Sync transactions from all connected accounts
budget sync

# Import from CSV (fallback for unsupported banks)
budget import --file transactions.csv --institution "My Bank" --account "Checking"

# Categorize transactions (3-tier: overrides > keywords > Claude)
budget categorize

# Override a category (system learns from corrections)
budget recategorize <transaction-id> "Dining Out"

# Add a budget rule
budget rule add --name "Dining budget" --category "Dining Out" --limit 200 --notify email
budget rule add --name "Large purchases" --type single_transaction --limit 500

# Manage rules
budget rule list
budget rule disable <id>
budget rule delete <id>

# Evaluate rules and send alerts
budget check

# Generate reports
budget report --month 2026-06
budget report --month 2026-06 --html > report.html
budget report --month 2026-06 --analyze        # includes AI analysis
budget report --month 2026-06 --analyze --email # send to your inbox

# Run as a daemon (syncs every 4h, categorizes hourly, monthly reports)
budget daemon
```

### AI Context

Create `~/.budget/context.txt` to give the AI analyst context about your finances. Lines starting with `#` are ignored.

```
I pay my credit card in full each month — no debt.
AWS charges are business expenses for my SaaS.
Amazon purchases are mostly necessities.
```

## Architecture

```
Plaid ──> Ingestion ──> SQLite ──> Categorizer ──> Rules Engine ──> Email Alerts
                                       |
                                  Claude API
                                  (batch, tier 3)
                                       |
                                Monthly Reports ──> Claude Analysis ──> HTML Email
```

### Categorization Pipeline

1. **Tier 1 — Override table**: Learned from user corrections. Free, instant.
2. **Tier 2 — Keyword map**: ~80 hardcoded merchant rules. Free, instant.
3. **Tier 3 — Claude API**: Batches of up to 50 transactions. ~$0.05/batch.

As you correct categorizations, Tier 1 grows and fewer transactions reach the API.

### Project Structure

```
budget-daemon/
├── cmd/budget/main.go           # CLI entrypoint (Cobra)
├── internal/
│   ├── config/                  # .env loading
│   ├── db/                      # SQLite schema + migrations
│   ├── crypto/                  # AES-256-GCM for Plaid tokens
│   ├── ingestion/               # Plaid, OFX, CSV sources + sync orchestrator
│   ├── categorizer/             # 3-tier AI categorization pipeline
│   ├── rules/                   # Budget rules engine
│   ├── notify/                  # Twilio SMS + SendGrid email
│   ├── reports/                 # Monthly rollups, HTML email, Claude analysis
│   └── daemon/                  # Cron-scheduled background daemon
├── deploy/                      # systemd + launchd service files
├── testdata/                    # Sample CSV for testing
├── .env.example                 # Template for configuration
└── go.mod
```

## Deploy to Raspberry Pi

```bash
# Cross-compile
GOOS=linux GOARCH=arm64 go build -o budget ./cmd/budget/

# Copy to Pi
scp budget pi@<pi-ip>:/opt/budget/budget
scp .env.example pi@<pi-ip>:/opt/budget/.env

# Install systemd service
sudo cp deploy/budget-daemon.service /etc/systemd/system/
sudo systemctl enable --now budget-daemon
```

See `deploy/README-deploy.txt` for full setup instructions.

## Cost

| Service | Monthly Cost |
|---|---|
| Plaid | $0 (personal/dev) |
| Claude API | $0.10 - $0.25 |
| SendGrid | $0 (free tier) |
| Raspberry Pi | $0 (one-time ~$120) |
| **Total** | **~$0.10 - $0.25/month** |

## License

MIT
