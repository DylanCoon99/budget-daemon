package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/dylancoon/budget-daemon/internal/categorizer"
	"github.com/dylancoon/budget-daemon/internal/config"
	"github.com/dylancoon/budget-daemon/internal/reports"
	"github.com/dylancoon/budget-daemon/internal/ingestion"
	"github.com/dylancoon/budget-daemon/internal/notify"
	"github.com/dylancoon/budget-daemon/internal/rules"
	"github.com/robfig/cron/v3"
)

// Daemon runs the budget automation on a schedule.
type Daemon struct {
	cfg        *config.Config
	db         *sql.DB
	cron       *cron.Cron
	syncer     *ingestion.Syncer
	pipeline   *categorizer.Pipeline
	engine     *rules.Engine
	dispatcher *notify.Dispatcher

	mu      sync.Mutex
	running bool
}

func New(cfg *config.Config, db *sql.DB) *Daemon {
	dispatcher := buildDispatcher(cfg)

	return &Daemon{
		cfg:        cfg,
		db:         db,
		syncer:     ingestion.NewSyncer(db, cfg.PlaidClientID, cfg.PlaidSecret, cfg.PlaidEnv, cfg.EncryptionKey),
		pipeline:   categorizer.NewPipeline(db, cfg.ClaudeAPIKey),
		engine:     rules.NewEngine(db, dispatcher),
		dispatcher: dispatcher,
	}
}

// Run starts the daemon and blocks until interrupted.
func (d *Daemon) Run(ctx context.Context) error {
	d.cron = cron.New(cron.WithSeconds(), cron.WithLogger(cron.VerbosePrintfLogger(slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug))))

	// Transaction sync: every 4 hours
	d.cron.AddFunc("0 0 */4 * * *", func() {
		d.runSyncCycle("scheduled-4h")
	})

	// Quick check: every hour on the half-hour (catches new transactions from manual imports)
	d.cron.AddFunc("0 30 * * * *", func() {
		d.runCategorizationAndRules("scheduled-30m")
	})

	// Daily balance refresh and full check: 7 AM
	d.cron.AddFunc("0 0 7 * * *", func() {
		d.runSyncCycle("daily-7am")
	})

	// Monthly rollup: 1st of each month at 8 AM
	d.cron.AddFunc("0 0 8 1 * *", func() {
		d.runMonthlyRollup()
	})

	d.cron.Start()

	fmt.Println("Budget daemon started.")
	fmt.Println("Schedule:")
	fmt.Println("  - Sync transactions:   every 4 hours")
	fmt.Println("  - Categorize + check:  every hour (:30)")
	fmt.Println("  - Daily full sync:     7:00 AM")
	fmt.Println("  - Monthly rollup:      1st of month, 8:00 AM")
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop.")

	// Run an initial sync cycle on startup
	go d.runSyncCycle("startup")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("received signal, shutting down", "signal", sig)
	case <-ctx.Done():
		slog.Info("context cancelled, shutting down")
	}

	// Graceful shutdown
	stopCtx := d.cron.Stop()
	<-stopCtx.Done()

	fmt.Println("Daemon stopped.")
	return nil
}

// runSyncCycle performs the full sync -> categorize -> rules pipeline.
func (d *Daemon) runSyncCycle(trigger string) {
	if !d.tryLock() {
		slog.Warn("sync cycle already running, skipping", "trigger", trigger)
		return
	}
	defer d.unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	slog.Info("sync cycle starting", "trigger", trigger)

	// Step 1: Sync transactions from all sources
	newTxns, err := d.syncer.SyncAll(ctx)
	if err != nil {
		slog.Error("sync failed", "trigger", trigger, "error", err)
	} else {
		slog.Info("sync complete", "new_transactions", newTxns)
	}

	// Step 2: Categorize any uncategorized transactions
	d.runCategorizationAndRules(trigger)

	slog.Info("sync cycle complete", "trigger", trigger, "duration", time.Since(start).Round(time.Millisecond))
}

// runCategorizationAndRules runs categorization and rule evaluation without syncing.
func (d *Daemon) runCategorizationAndRules(trigger string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	categorized, err := d.pipeline.CategorizeAll(ctx)
	if err != nil {
		slog.Error("categorization failed", "trigger", trigger, "error", err)
	} else if categorized > 0 {
		slog.Info("categorized transactions", "count", categorized)
	}

	// Step 3: Evaluate budget rules
	alerts, err := d.engine.EvaluateAll(ctx)
	if err != nil {
		slog.Error("rule evaluation failed", "trigger", trigger, "error", err)
	} else if alerts > 0 {
		slog.Info("alerts triggered", "count", alerts)
	}
}

func (d *Daemon) runMonthlyRollup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Report on the previous month
	prev := time.Now().AddDate(0, -1, 0)
	yearMonth := prev.Format("2006-01")

	slog.Info("monthly rollup starting", "month", yearMonth)

	report, err := reports.GenerateRollup(ctx, d.db, yearMonth)
	if err != nil {
		slog.Error("monthly rollup failed", "error", err)
		return
	}

	// Run Claude analysis
	if d.cfg.ClaudeAPIKey != "" {
		analysis, err := reports.AnalyzeWithClaude(ctx, d.cfg.ClaudeAPIKey, report)
		if err != nil {
			slog.Error("monthly analysis failed", "error", err)
		} else {
			report.Analysis = analysis
		}
	}

	// Send email report
	if d.dispatcher != nil {
		alert := notify.Alert{
			Subject: fmt.Sprintf("Budget Report: %s", yearMonth),
			Body:    report.FormatHTML(),
			Channel: "email",
		}
		if err := d.dispatcher.Dispatch(ctx, alert); err != nil {
			slog.Error("failed to email monthly report", "error", err)
		} else {
			slog.Info("monthly report emailed", "month", yearMonth)
		}
	}

	slog.Info("monthly rollup complete", "month", yearMonth)
}

func (d *Daemon) tryLock() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return false
	}
	d.running = true
	return true
}

func (d *Daemon) unlock() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = false
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
