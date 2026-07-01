package main

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dylancoon/budget-daemon/internal/categorizer"
	"github.com/dylancoon/budget-daemon/internal/config"
	"github.com/dylancoon/budget-daemon/internal/daemon"
	"github.com/dylancoon/budget-daemon/internal/db"
	"github.com/dylancoon/budget-daemon/internal/ingestion"
	"github.com/dylancoon/budget-daemon/internal/notify"
	"github.com/dylancoon/budget-daemon/internal/reports"
	"github.com/dylancoon/budget-daemon/internal/rules"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "budget",
		Short: "Personal budget automation daemon",
	}

	root.AddCommand(
		syncCmd(),
		categorizeCmd(),
		recategorizeCmd(),
		checkCmd(),
		daemonCmd(),
		ruleCmd(),
		reportCmd(),
		importCmd(),
		setupCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync transactions from all connected accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			syncer := ingestion.NewSyncer(database, cfg.PlaidClientID, cfg.PlaidSecret, cfg.PlaidEnv, cfg.EncryptionKey)

			account, _ := cmd.Flags().GetString("account")

			fmt.Println("Syncing transactions...")
			var count int
			if account != "" {
				count, err = syncer.SyncInstitution(cmd.Context(), account)
			} else {
				count, err = syncer.SyncAll(cmd.Context())
			}
			if err != nil {
				return fmt.Errorf("sync: %w", err)
			}

			fmt.Printf("Sync complete. %d new transactions.\n", count)
			return nil
		},
	}
	cmd.Flags().String("account", "", "Sync a specific institution by name (e.g., 'Discover')")
	return cmd
}

func categorizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "categorize",
		Short: "Run AI categorization on uncategorized transactions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			pipeline := categorizer.NewPipeline(database, cfg.ClaudeAPIKey)
			count, err := pipeline.CategorizeAll(cmd.Context())
			if err != nil {
				return fmt.Errorf("categorize: %w", err)
			}

			fmt.Printf("Categorized %d transactions.\n", count)
			return nil
		},
	}
}

func recategorizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recategorize [transaction-id] [category]",
		Short: "Override a transaction's category (system learns from this)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			pipeline := categorizer.NewPipeline(database, cfg.ClaudeAPIKey)
			if err := pipeline.Recategorize(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}

			fmt.Printf("Recategorized transaction %s as %q.\n", args[0], args[1])
			return nil
		},
	}
}

func buildDispatcher(cfg *config.Config) *notify.Dispatcher {
	var sms notify.Notifier
	var email notify.Notifier

	if cfg.TwilioSID != "" {
		sms = notify.NewTwilioSMS(cfg.TwilioSID, cfg.TwilioToken, cfg.TwilioFrom, cfg.NotifyPhone)
	}
	if cfg.SendGridAPIKey != "" {
		email = notify.NewSendGridEmail(cfg.SendGridAPIKey, cfg.NotifyEmail, cfg.NotifyEmail)
	}

	return notify.NewDispatcher(sms, email)
}

func checkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Evaluate all budget rules now and send alerts if triggered",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			engine := rules.NewEngine(database, buildDispatcher(cfg))
			count, err := engine.EvaluateAll(cmd.Context())
			if err != nil {
				return fmt.Errorf("check rules: %w", err)
			}

			if count > 0 {
				fmt.Printf("Triggered %d alert(s).\n", count)
			} else {
				fmt.Println("All budgets within limits.")
			}
			return nil
		},
	}
}

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run as a background daemon with scheduled syncs",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			d := daemon.New(cfg, database)
			return d.Run(cmd.Context())
		},
	}
}

func ruleCmd() *cobra.Command {
	rule := &cobra.Command{
		Use:   "rule",
		Short: "Manage budget rules",
	}

	// budget rule add
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a budget rule",
		Example: `  budget rule add --name "Dining budget" --type category_monthly_limit --category "Dining Out" --limit 200 --alert-at 80,100 --notify sms
  budget rule add --name "Total spending" --type total_monthly_limit --limit 3000 --notify both
  budget rule add --name "Large purchases" --type single_transaction --limit 500 --notify sms`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			name, _ := cmd.Flags().GetString("name")
			ruleType, _ := cmd.Flags().GetString("type")
			category, _ := cmd.Flags().GetString("category")
			limit, _ := cmd.Flags().GetFloat64("limit")
			period, _ := cmd.Flags().GetString("period")
			alertAt, _ := cmd.Flags().GetString("alert-at")
			notifyChannel, _ := cmd.Flags().GetString("notify")

			if name == "" || ruleType == "" || limit <= 0 {
				return fmt.Errorf("--name, --type, and --limit are required")
			}

			// Parse alert percentages
			pcts := parseAlertPcts(alertAt)

			engine := rules.NewEngine(database, nil)
			if err := engine.AddRule(cmd.Context(), name, ruleType, category, limit, period, pcts, notifyChannel); err != nil {
				return err
			}

			fmt.Printf("Rule %q added.\n", name)
			return nil
		},
	}
	addCmd.Flags().String("name", "", "Rule name")
	addCmd.Flags().String("type", "category_monthly_limit", "Rule type: category_monthly_limit, total_monthly_limit, single_transaction")
	addCmd.Flags().String("category", "", "Category name (for category rules)")
	addCmd.Flags().Float64("limit", 0, "Dollar threshold")
	addCmd.Flags().String("period", "monthly", "Budget period: monthly, weekly")
	addCmd.Flags().String("alert-at", "80,100", "Alert at these percentages (comma-separated)")
	addCmd.Flags().String("notify", "email", "Notification channel: sms, email, both")

	// budget rule list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all budget rules",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			engine := rules.NewEngine(database, nil)
			ruleList, err := engine.ListRules(cmd.Context())
			if err != nil {
				return err
			}

			if len(ruleList) == 0 {
				fmt.Println("No rules configured. Use 'budget rule add' to create one.")
				return nil
			}

			fmt.Printf("%-4s %-25s %-25s %-10s %-8s %-10s %s\n",
				"ID", "Name", "Type", "Limit", "Period", "Alert At", "Channel")
			fmt.Println(strings.Repeat("-", 100))
			for _, r := range ruleList {
				cat := ""
				if r.CategoryName != "" {
					cat = fmt.Sprintf(" (%s)", r.CategoryName)
				}
				pctStrs := make([]string, len(r.NotifyAtPct))
				for i, p := range r.NotifyAtPct {
					pctStrs[i] = fmt.Sprintf("%d%%", p)
				}
				fmt.Printf("%-4d %-25s %-25s $%-9.0f %-8s %-10s %s\n",
					r.ID, r.Name+cat, r.RuleType, r.Threshold, r.Period,
					strings.Join(pctStrs, ","), r.NotifyChannel)
			}
			return nil
		},
	}

	// budget rule disable
	disableCmd := &cobra.Command{
		Use:   "disable [rule-id]",
		Short: "Disable a budget rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid rule ID: %s", args[0])
			}

			engine := rules.NewEngine(database, nil)
			if err := engine.DisableRule(cmd.Context(), id); err != nil {
				return err
			}

			fmt.Printf("Rule %d disabled.\n", id)
			return nil
		},
	}

	// budget rule delete
	deleteCmd := &cobra.Command{
		Use:   "delete [rule-id]",
		Short: "Delete a budget rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid rule ID: %s", args[0])
			}

			engine := rules.NewEngine(database, nil)
			if err := engine.DeleteRule(cmd.Context(), id); err != nil {
				return err
			}

			fmt.Printf("Rule %d deleted.\n", id)
			return nil
		},
	}

	rule.AddCommand(addCmd, listCmd, disableCmd, deleteCmd)
	return rule
}

func parseAlertPcts(s string) []int {
	var result []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimSuffix(part, "%")
		if v, err := strconv.Atoi(part); err == nil {
			result = append(result, v)
		}
	}
	if len(result) == 0 {
		return []int{80, 100}
	}
	return result
}

func reportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Generate a monthly spending report",
		Example: `  budget report --month 2026-06
  budget report --month 2026-06 --html > report.html
  budget report --month 2026-06 --analyze`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			month, _ := cmd.Flags().GetString("month")
			if month == "" {
				// Default to previous month
				now := time.Now()
				prev := now.AddDate(0, -1, 0)
				month = prev.Format("2006-01")
			}

			report, err := reports.GenerateRollup(cmd.Context(), database, month)
			if err != nil {
				return fmt.Errorf("generate report: %w", err)
			}

			// Claude analysis
			analyze, _ := cmd.Flags().GetBool("analyze")
			if analyze && cfg.ClaudeAPIKey != "" {
				fmt.Fprintf(os.Stderr, "Running AI analysis...\n")
				analysis, err := reports.AnalyzeWithClaude(cmd.Context(), cfg.ClaudeAPIKey, report)
				if err != nil {
					fmt.Fprintf(os.Stderr, "AI analysis failed: %v\n", err)
				} else {
					report.Analysis = analysis
				}
			}

			// Output
			html, _ := cmd.Flags().GetBool("html")
			if html {
				fmt.Print(report.FormatHTML())
			} else {
				fmt.Print(report.FormatText())
			}

			// Send email if requested
			sendEmail, _ := cmd.Flags().GetBool("email")
			if sendEmail && cfg.SendGridAPIKey != "" && cfg.NotifyEmail != "" {
				emailNotifier := notify.NewSendGridEmail(cfg.SendGridAPIKey, "budget@localhost", cfg.NotifyEmail)
				alert := notify.Alert{
					Subject: fmt.Sprintf("Budget Report: %s", month),
					Body:    report.FormatHTML(),
					Channel: "email",
				}
				if err := emailNotifier.Send(cmd.Context(), alert); err != nil {
					return fmt.Errorf("send email: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Report emailed to %s\n", cfg.NotifyEmail)
			}

			return nil
		},
	}
	cmd.Flags().String("month", "", "Month to report on (YYYY-MM, default: previous month)")
	cmd.Flags().Bool("html", false, "Output as HTML instead of plain text")
	cmd.Flags().Bool("analyze", false, "Include Claude AI analysis of spending patterns")
	cmd.Flags().Bool("email", false, "Email the report (requires SendGrid config)")
	return cmd
}

func importCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import transactions from a CSV file",
		Example: `  budget import --file cnb-july.csv --institution "CNB Bank" --account "CNB Checking" --type checking`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			filePath, _ := cmd.Flags().GetString("file")
			instName, _ := cmd.Flags().GetString("institution")
			acctName, _ := cmd.Flags().GetString("account")
			acctType, _ := cmd.Flags().GetString("type")

			if filePath == "" || instName == "" || acctName == "" {
				return fmt.Errorf("--file, --institution, and --account are required")
			}

			// Register the institution/account if needed
			syncer := ingestion.NewSyncer(database, cfg.PlaidClientID, cfg.PlaidSecret, cfg.PlaidEnv, cfg.EncryptionKey)
			accountID, err := syncer.RegisterCSVInstitution(cmd.Context(), instName, acctName, acctType)
			if err != nil {
				return fmt.Errorf("register account: %w", err)
			}

			// Parse CSV
			csvSource := &ingestion.CSVSource{
				FilePath:  filePath,
				AccountID: accountID,
			}

			txns, err := csvSource.Sync(cmd.Context())
			if err != nil {
				return fmt.Errorf("parse csv: %w", err)
			}

			// Insert transactions
			inserted, err := syncer.InsertTransactions(cmd.Context(), txns)
			if err != nil {
				return fmt.Errorf("insert: %w", err)
			}

			fmt.Printf("Imported %d new transactions from %s.\n", inserted, filePath)
			return nil
		},
	}
	cmd.Flags().String("file", "", "Path to CSV file")
	cmd.Flags().String("institution", "", "Institution name (e.g., 'CNB Bank')")
	cmd.Flags().String("account", "", "Account name (e.g., 'CNB Checking')")
	cmd.Flags().String("type", "checking", "Account type: checking, savings, credit")
	return cmd
}

func setupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Connect a financial account",
	}

	// budget setup plaid
	plaidCmd := &cobra.Command{
		Use:   "plaid [institution-name]",
		Short: "Connect a bank account via Plaid Link",
		Example: `  budget setup plaid "Discover"
  budget setup plaid "Fidelity"
  budget setup plaid "CNB Bank"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			if cfg.PlaidClientID == "" || cfg.PlaidSecret == "" {
				return fmt.Errorf("PLAID_CLIENT_ID and PLAID_SECRET must be set in .env")
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			instName := args[0]

			baseURL := "https://sandbox.plaid.com"
			switch cfg.PlaidEnv {
			case "development":
				baseURL = "https://development.plaid.com"
			case "production":
				baseURL = "https://production.plaid.com"
			}

			setup := &ingestion.PlaidSetup{
				ClientID: cfg.PlaidClientID,
				Secret:   cfg.PlaidSecret,
				BaseURL:  baseURL,
			}

			fmt.Printf("Connecting %s via Plaid...\n", instName)
			result, err := setup.RunLinkFlow(cmd.Context())
			if err != nil {
				return fmt.Errorf("plaid link: %w", err)
			}

			syncer := ingestion.NewSyncer(database, cfg.PlaidClientID, cfg.PlaidSecret, cfg.PlaidEnv, cfg.EncryptionKey)
			if err := syncer.RegisterPlaidInstitution(cmd.Context(), instName, result); err != nil {
				return fmt.Errorf("register: %w", err)
			}

			fmt.Printf("\nSuccessfully connected %s!\n", instName)
			fmt.Println("Accounts:")
			for _, a := range result.Accounts {
				fmt.Printf("  - %s (%s/%s)\n", a.Name, a.Type, a.Subtype)
			}
			fmt.Println("\nRun 'budget sync' to pull transactions.")
			return nil
		},
	}

	// budget setup csv
	csvCmd := &cobra.Command{
		Use:   "csv [institution-name] [account-name]",
		Short: "Register an institution for CSV-based imports",
		Example: `  budget setup csv "CNB Bank" "CNB Checking" --type checking`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			acctType, _ := cmd.Flags().GetString("type")

			syncer := ingestion.NewSyncer(database, cfg.PlaidClientID, cfg.PlaidSecret, cfg.PlaidEnv, cfg.EncryptionKey)
			_, err = syncer.RegisterCSVInstitution(cmd.Context(), args[0], args[1], acctType)
			if err != nil {
				return err
			}

			fmt.Printf("Registered %s / %s for CSV imports.\n", args[0], args[1])
			fmt.Println("Use 'budget import --file <path> --institution \"" + args[0] + "\" --account \"" + args[1] + "\"' to import.")
			return nil
		},
	}
	csvCmd.Flags().String("type", "checking", "Account type: checking, savings, credit")

	// budget setup status
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show connected institutions and accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := db.Open(cfg.DatabasePath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			rows, err := database.QueryContext(cmd.Context(), `
				SELECT i.name, i.source_type, i.last_synced_at,
				       a.name, a.account_type, a.current_balance
				FROM institutions i
				LEFT JOIN accounts a ON a.institution_id = i.id
				ORDER BY i.name, a.name
			`)
			if err != nil {
				return err
			}
			defer rows.Close()

			currentInst := ""
			found := false
			for rows.Next() {
				found = true
				var instName, sourceType string
				var lastSynced, acctName, acctType sql.NullString
				var balance sql.NullFloat64

				rows.Scan(&instName, &sourceType, &lastSynced, &acctName, &acctType, &balance)

				if instName != currentInst {
					if currentInst != "" {
						fmt.Println()
					}
					syncedAt := "never"
					if lastSynced.Valid {
						syncedAt = lastSynced.String
					}
					fmt.Printf("%s [%s] (last synced: %s)\n", instName, sourceType, syncedAt)
					currentInst = instName
				}

				if acctName.Valid {
					bal := "unknown"
					if balance.Valid {
						bal = fmt.Sprintf("$%.2f", balance.Float64)
					}
					fmt.Printf("  - %s (%s) balance: %s\n", acctName.String, acctType.String, bal)
				}
			}

			if !found {
				fmt.Println("No institutions configured. Run 'budget setup plaid' or 'budget setup csv' to add one.")
			}
			return nil
		},
	}

	cmd.AddCommand(plaidCmd, csvCmd, statusCmd)
	return cmd
}
