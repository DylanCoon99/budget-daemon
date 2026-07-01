package rules

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dylancoon/budget-daemon/internal/notify"
)

// Engine evaluates budget rules against the current state of transactions
// and fires alerts when thresholds are crossed.
type Engine struct {
	db         *sql.DB
	dispatcher *notify.Dispatcher
}

func NewEngine(db *sql.DB, dispatcher *notify.Dispatcher) *Engine {
	return &Engine{db: db, dispatcher: dispatcher}
}

// Rule represents a budget rule from the database.
type Rule struct {
	ID            int
	Name          string
	RuleType      string // "category_monthly_limit", "total_monthly_limit", "single_transaction", "balance_threshold"
	CategoryID    *int
	CategoryName  string
	Threshold     float64
	Period        string // "monthly", "weekly"
	NotifyAtPct   []int  // e.g., [80, 100]
	NotifyChannel string // "sms", "email", "both"
	IsActive      bool
}

// EvaluateAll loads all active rules and checks them against current spending.
func (e *Engine) EvaluateAll(ctx context.Context) (int, error) {
	rules, err := e.loadRules(ctx)
	if err != nil {
		return 0, fmt.Errorf("load rules: %w", err)
	}

	if len(rules) == 0 {
		return 0, nil
	}

	alertCount := 0

	for _, rule := range rules {
		switch rule.RuleType {
		case "category_monthly_limit":
			n, err := e.evalCategoryLimit(ctx, rule)
			if err != nil {
				slog.Error("rule evaluation failed", "rule", rule.Name, "error", err)
				continue
			}
			alertCount += n

		case "total_monthly_limit":
			n, err := e.evalTotalLimit(ctx, rule)
			if err != nil {
				slog.Error("rule evaluation failed", "rule", rule.Name, "error", err)
				continue
			}
			alertCount += n

		case "single_transaction":
			n, err := e.evalSingleTransaction(ctx, rule)
			if err != nil {
				slog.Error("rule evaluation failed", "rule", rule.Name, "error", err)
				continue
			}
			alertCount += n
		}
	}

	return alertCount, nil
}

func (e *Engine) evalCategoryLimit(ctx context.Context, rule Rule) (int, error) {
	if rule.CategoryID == nil {
		return 0, nil
	}

	periodStart, _ := currentPeriodBounds(rule.Period)
	spent, err := e.sumSpending(ctx, rule.CategoryID, periodStart)
	if err != nil {
		return 0, err
	}

	return e.checkThresholds(ctx, rule, spent, periodStart)
}

func (e *Engine) evalTotalLimit(ctx context.Context, rule Rule) (int, error) {
	periodStart, _ := currentPeriodBounds(rule.Period)
	spent, err := e.sumSpending(ctx, nil, periodStart)
	if err != nil {
		return 0, err
	}

	return e.checkThresholds(ctx, rule, spent, periodStart)
}

func (e *Engine) evalSingleTransaction(ctx context.Context, rule Rule) (int, error) {
	// Find transactions today that exceed the threshold and haven't been alerted
	today := time.Now().Format("2006-01-02")

	rows, err := e.db.QueryContext(ctx, `
		SELECT t.id, t.description, t.amount, t.merchant_name
		FROM transactions t
		WHERE t.date = ?
		AND ABS(t.amount) >= ?
		AND t.amount < 0
		AND NOT EXISTS (
			SELECT 1 FROM alert_history ah
			WHERE ah.rule_id = ? AND ah.period_start = t.date
			AND ah.message LIKE '%' || t.id || '%'
		)
	`, today, rule.Threshold, rule.ID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	alertCount := 0
	for rows.Next() {
		var txnID, desc string
		var amount float64
		var merchant sql.NullString
		if err := rows.Scan(&txnID, &desc, &amount, &merchant); err != nil {
			continue
		}

		name := desc
		if merchant.Valid && merchant.String != "" {
			name = merchant.String
		}

		msg := fmt.Sprintf("Large transaction: $%.2f at %s", math.Abs(amount), name)

		alert := notify.Alert{
			Subject: msg,
			Body:    fmt.Sprintf("Transaction ID: %s\nAmount: $%.2f\nMerchant: %s\nDate: %s", txnID, math.Abs(amount), name, today),
			Channel: rule.NotifyChannel,
		}

		if err := e.dispatcher.Dispatch(ctx, alert); err != nil {
			slog.Error("failed to send alert", "error", err)
			continue
		}

		// Record that we alerted for this transaction
		e.recordAlert(ctx, rule.ID, today, 100, math.Abs(amount), msg)
		alertCount++
	}

	return alertCount, nil
}

func (e *Engine) checkThresholds(ctx context.Context, rule Rule, spent float64, periodStart string) (int, error) {
	alertCount := 0
	daysLeft := daysLeftInPeriod(rule.Period)

	for _, pct := range rule.NotifyAtPct {
		threshold := rule.Threshold * float64(pct) / 100.0

		if spent >= threshold && !e.alreadyAlerted(ctx, rule.ID, periodStart, pct) {
			actualPct := int(spent / rule.Threshold * 100)

			var msg string
			if rule.CategoryName != "" {
				msg = fmt.Sprintf("%s: $%.0f / $%.0f (%d%%). %d days left.",
					rule.Name, spent, rule.Threshold, actualPct, daysLeft)
			} else {
				msg = fmt.Sprintf("%s: $%.0f / $%.0f (%d%%). %d days left.",
					rule.Name, spent, rule.Threshold, actualPct, daysLeft)
			}

			if pct >= 100 {
				msg = fmt.Sprintf("BUDGET EXCEEDED — %s", msg)
			}

			alert := notify.Alert{
				Subject: msg,
				Channel: rule.NotifyChannel,
			}

			if err := e.dispatcher.Dispatch(ctx, alert); err != nil {
				slog.Error("failed to send alert", "rule", rule.Name, "error", err)
				continue
			}

			e.recordAlert(ctx, rule.ID, periodStart, pct, spent, msg)
			alertCount++
			slog.Info("alert fired", "rule", rule.Name, "pct", pct, "spent", spent)
		}
	}

	return alertCount, nil
}

func (e *Engine) sumSpending(ctx context.Context, categoryID *int, since string) (float64, error) {
	var spent float64
	var err error

	if categoryID != nil {
		err = e.db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(ABS(amount)), 0)
			FROM transactions
			WHERE category_id = ? AND date >= ? AND amount < 0
		`, *categoryID, since).Scan(&spent)
	} else {
		err = e.db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(ABS(amount)), 0)
			FROM transactions
			WHERE date >= ? AND amount < 0
		`, since).Scan(&spent)
	}

	return spent, err
}

func (e *Engine) alreadyAlerted(ctx context.Context, ruleID int, periodStart string, pct int) bool {
	var count int
	e.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM alert_history
		WHERE rule_id = ? AND period_start = ? AND threshold_pct = ?
	`, ruleID, periodStart, pct).Scan(&count)
	return count > 0
}

func (e *Engine) recordAlert(ctx context.Context, ruleID int, periodStart string, pct int, amount float64, msg string) {
	_, err := e.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO alert_history (rule_id, period_start, threshold_pct, current_amount, message, channel)
		VALUES (?, ?, ?, ?, ?, 'logged')
	`, ruleID, periodStart, pct, amount, msg)
	if err != nil {
		slog.Error("failed to record alert", "error", err)
	}
}

func (e *Engine) loadRules(ctx context.Context) ([]Rule, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT br.id, br.name, br.rule_type, br.category_id, br.threshold,
		       br.period, br.notify_at_pct, br.notify_channel,
		       COALESCE(c.name, '')
		FROM budget_rules br
		LEFT JOIN categories c ON c.id = br.category_id
		WHERE br.is_active = 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		var r Rule
		var catID sql.NullInt64
		var pctStr string

		if err := rows.Scan(&r.ID, &r.Name, &r.RuleType, &catID, &r.Threshold,
			&r.Period, &pctStr, &r.NotifyChannel, &r.CategoryName); err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}

		if catID.Valid {
			id := int(catID.Int64)
			r.CategoryID = &id
		}

		r.NotifyAtPct = parsePctList(pctStr)
		r.IsActive = true
		rules = append(rules, r)
	}

	return rules, nil
}

// AddRule inserts a new budget rule.
func (e *Engine) AddRule(ctx context.Context, name, ruleType string, categoryName string, threshold float64, period string, alertPcts []int, channel string) error {
	var categoryID *int

	if categoryName != "" {
		var id int
		err := e.db.QueryRowContext(ctx, `SELECT id FROM categories WHERE name = ?`, categoryName).Scan(&id)
		if err != nil {
			return fmt.Errorf("unknown category %q: %w", categoryName, err)
		}
		categoryID = &id
	}

	pctStr := intSliceToStr(alertPcts)

	_, err := e.db.ExecContext(ctx, `
		INSERT INTO budget_rules (name, rule_type, category_id, threshold, period, notify_at_pct, notify_channel)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, name, ruleType, categoryID, threshold, period, pctStr, channel)

	return err
}

// ListRules returns all rules for display.
func (e *Engine) ListRules(ctx context.Context) ([]Rule, error) {
	return e.loadRules(ctx)
}

// DisableRule deactivates a rule by ID.
func (e *Engine) DisableRule(ctx context.Context, ruleID int) error {
	res, err := e.db.ExecContext(ctx, `UPDATE budget_rules SET is_active = 0 WHERE id = ?`, ruleID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("rule %d not found", ruleID)
	}
	return nil
}

// DeleteRule removes a rule by ID.
func (e *Engine) DeleteRule(ctx context.Context, ruleID int) error {
	// Delete alert history first
	e.db.ExecContext(ctx, `DELETE FROM alert_history WHERE rule_id = ?`, ruleID)

	res, err := e.db.ExecContext(ctx, `DELETE FROM budget_rules WHERE id = ?`, ruleID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("rule %d not found", ruleID)
	}
	return nil
}

func currentPeriodBounds(period string) (start string, end string) {
	now := time.Now()
	switch period {
	case "weekly":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		monday := now.AddDate(0, 0, -(weekday - 1))
		sunday := monday.AddDate(0, 0, 6)
		return monday.Format("2006-01-02"), sunday.Format("2006-01-02")
	default: // monthly
		firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		lastOfMonth := firstOfMonth.AddDate(0, 1, -1)
		return firstOfMonth.Format("2006-01-02"), lastOfMonth.Format("2006-01-02")
	}
}

func daysLeftInPeriod(period string) int {
	now := time.Now()
	switch period {
	case "weekly":
		return 7 - int(now.Weekday())
	default:
		lastDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location())
		return lastDay.Day() - now.Day()
	}
}

func parsePctList(s string) []int {
	var result []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if v, err := strconv.Atoi(part); err == nil {
			result = append(result, v)
		}
	}
	if len(result) == 0 {
		return []int{80, 100}
	}
	return result
}

func intSliceToStr(vals []int) string {
	strs := make([]string, len(vals))
	for i, v := range vals {
		strs[i] = strconv.Itoa(v)
	}
	return strings.Join(strs, ",")
}
