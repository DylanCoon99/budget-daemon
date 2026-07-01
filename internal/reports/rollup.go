package reports

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// MonthlyReport holds all data for a single month's report.
type MonthlyReport struct {
	YearMonth    string // "2026-07"
	GeneratedAt  time.Time
	Summary      Summary
	ByCategory   []CategoryRow
	TopMerchants []MerchantRow
	BudgetPerf   []BudgetPerfRow
	Uncategorized []TransactionRow
	Accounts     []AccountBalance
	Analysis     string // Claude-generated analysis
}

type Summary struct {
	TotalIncome   float64
	TotalExpenses float64
	NetSavings    float64
	SavingsRate   float64 // percentage
	TxnCount      int
}

type CategoryRow struct {
	Name         string
	Total        float64
	Count        int
	AvgAmount    float64
	PrevMonth    float64
	DeltaPct     float64 // percentage change from previous month
}

type MerchantRow struct {
	Name  string
	Total float64
	Count int
}

type BudgetPerfRow struct {
	RuleName  string
	Category  string
	Limit     float64
	Spent     float64
	Pct       float64
	Bar       string // ASCII bar chart
}

type TransactionRow struct {
	ID          string
	Date        string
	Description string
	Amount      float64
}

type AccountBalance struct {
	Name    string
	Type    string
	Balance float64
}

// GenerateRollup builds a MonthlyReport for the given month.
func GenerateRollup(ctx context.Context, db *sql.DB, yearMonth string) (*MonthlyReport, error) {
	report := &MonthlyReport{
		YearMonth:   yearMonth,
		GeneratedAt: time.Now(),
	}

	// Parse the month to get date boundaries
	start, end, err := monthBounds(yearMonth)
	if err != nil {
		return nil, err
	}

	prevMonth := previousMonth(yearMonth)
	prevStart, prevEnd, _ := monthBounds(prevMonth)

	// Summary
	if err := buildSummary(ctx, db, start, end, &report.Summary); err != nil {
		return nil, fmt.Errorf("summary: %w", err)
	}

	// Category breakdown
	report.ByCategory, err = buildCategoryBreakdown(ctx, db, start, end, prevStart, prevEnd)
	if err != nil {
		return nil, fmt.Errorf("categories: %w", err)
	}

	// Top merchants
	report.TopMerchants, err = buildTopMerchants(ctx, db, start, end)
	if err != nil {
		return nil, fmt.Errorf("merchants: %w", err)
	}

	// Budget performance
	report.BudgetPerf, err = buildBudgetPerformance(ctx, db, start, end)
	if err != nil {
		return nil, fmt.Errorf("budget perf: %w", err)
	}

	// Uncategorized transactions
	report.Uncategorized, err = buildUncategorized(ctx, db, start, end)
	if err != nil {
		return nil, fmt.Errorf("uncategorized: %w", err)
	}

	// Account balances
	report.Accounts, err = buildAccountBalances(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("balances: %w", err)
	}

	// Persist rollup data
	persistRollup(ctx, db, yearMonth, report)

	return report, nil
}

func buildSummary(ctx context.Context, db *sql.DB, start, end string, s *Summary) error {
	// Total income (positive amounts)
	db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0), COUNT(*)
		FROM transactions WHERE date >= ? AND date <= ? AND amount > 0
	`, start, end).Scan(&s.TotalIncome, &s.TxnCount)

	// Total expenses (negative amounts)
	var expenseCount int
	db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(ABS(amount)), 0), COUNT(*)
		FROM transactions WHERE date >= ? AND date <= ? AND amount < 0
	`, start, end).Scan(&s.TotalExpenses, &expenseCount)

	s.TxnCount += expenseCount
	s.NetSavings = s.TotalIncome - s.TotalExpenses
	if s.TotalIncome > 0 {
		s.SavingsRate = (s.NetSavings / s.TotalIncome) * 100
	}

	return nil
}

func buildCategoryBreakdown(ctx context.Context, db *sql.DB, start, end, prevStart, prevEnd string) ([]CategoryRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(c.name, 'Uncategorized'), SUM(ABS(t.amount)), COUNT(*), AVG(ABS(t.amount))
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.date >= ? AND t.date <= ? AND t.amount < 0
		GROUP BY c.name
		ORDER BY SUM(ABS(t.amount)) DESC
	`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CategoryRow
	for rows.Next() {
		var r CategoryRow
		rows.Scan(&r.Name, &r.Total, &r.Count, &r.AvgAmount)

		// Get previous month's total for this category
		db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(ABS(t.amount)), 0)
			FROM transactions t
			LEFT JOIN categories c ON c.id = t.category_id
			WHERE t.date >= ? AND t.date <= ? AND t.amount < 0
			AND COALESCE(c.name, 'Uncategorized') = ?
		`, prevStart, prevEnd, r.Name).Scan(&r.PrevMonth)

		if r.PrevMonth > 0 {
			r.DeltaPct = ((r.Total - r.PrevMonth) / r.PrevMonth) * 100
		}

		results = append(results, r)
	}

	return results, nil
}

func buildTopMerchants(ctx context.Context, db *sql.DB, start, end string) ([]MerchantRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(merchant_name, description), SUM(ABS(amount)), COUNT(*)
		FROM transactions
		WHERE date >= ? AND date <= ? AND amount < 0
		GROUP BY COALESCE(merchant_name, description)
		ORDER BY SUM(ABS(amount)) DESC
		LIMIT 10
	`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []MerchantRow
	for rows.Next() {
		var r MerchantRow
		rows.Scan(&r.Name, &r.Total, &r.Count)
		results = append(results, r)
	}

	return results, nil
}

func buildBudgetPerformance(ctx context.Context, db *sql.DB, start, end string) ([]BudgetPerfRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT br.name, COALESCE(c.name, 'All'), br.threshold
		FROM budget_rules br
		LEFT JOIN categories c ON c.id = br.category_id
		WHERE br.is_active = 1 AND br.period = 'monthly'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []BudgetPerfRow
	for rows.Next() {
		var r BudgetPerfRow
		rows.Scan(&r.RuleName, &r.Category, &r.Limit)

		// Calculate spent
		if r.Category != "All" {
			db.QueryRowContext(ctx, `
				SELECT COALESCE(SUM(ABS(t.amount)), 0)
				FROM transactions t
				JOIN categories c ON c.id = t.category_id
				WHERE t.date >= ? AND t.date <= ? AND t.amount < 0 AND c.name = ?
			`, start, end, r.Category).Scan(&r.Spent)
		} else {
			db.QueryRowContext(ctx, `
				SELECT COALESCE(SUM(ABS(amount)), 0)
				FROM transactions
				WHERE date >= ? AND date <= ? AND amount < 0
			`, start, end).Scan(&r.Spent)
		}

		r.Pct = (r.Spent / r.Limit) * 100
		r.Bar = buildBar(r.Pct)

		results = append(results, r)
	}

	return results, nil
}

func buildUncategorized(ctx context.Context, db *sql.DB, start, end string) ([]TransactionRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, date, description, amount
		FROM transactions
		WHERE date >= ? AND date <= ? AND category_id IS NULL
		ORDER BY date DESC
		LIMIT 20
	`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TransactionRow
	for rows.Next() {
		var r TransactionRow
		rows.Scan(&r.ID, &r.Date, &r.Description, &r.Amount)
		results = append(results, r)
	}

	return results, nil
}

func buildAccountBalances(ctx context.Context, db *sql.DB) ([]AccountBalance, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name, account_type, COALESCE(current_balance, 0)
		FROM accounts
		WHERE is_active = 1
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []AccountBalance
	for rows.Next() {
		var r AccountBalance
		rows.Scan(&r.Name, &r.Type, &r.Balance)
		results = append(results, r)
	}

	return results, nil
}

func persistRollup(ctx context.Context, db *sql.DB, yearMonth string, report *MonthlyReport) {
	// Clear existing rollup data for this month
	db.ExecContext(ctx, `DELETE FROM monthly_rollups WHERE year_month = ?`, yearMonth)

	for _, cat := range report.ByCategory {
		db.ExecContext(ctx, `
			INSERT INTO monthly_rollups (year_month, category_id, transaction_count, total_amount, avg_amount)
			SELECT ?, c.id, ?, ?, ?
			FROM categories c WHERE c.name = ?
		`, yearMonth, cat.Count, cat.Total, cat.AvgAmount, cat.Name)
	}
}

func buildBar(pct float64) string {
	width := 20
	filled := int(math.Min(pct/100.0*float64(width), float64(width)))
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	return fmt.Sprintf("[%s]", bar)
}

func monthBounds(yearMonth string) (string, string, error) {
	t, err := time.Parse("2006-01", yearMonth)
	if err != nil {
		return "", "", fmt.Errorf("invalid month format %q (expected YYYY-MM): %w", yearMonth, err)
	}
	start := t.Format("2006-01-02")
	end := t.AddDate(0, 1, -1).Format("2006-01-02")
	return start, end, nil
}

func previousMonth(yearMonth string) string {
	t, _ := time.Parse("2006-01", yearMonth)
	return t.AddDate(0, -1, 0).Format("2006-01")
}

// FormatText generates a plain-text version of the report for CLI output.
func (r *MonthlyReport) FormatText() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Budget Report: %s\n", r.YearMonth))
	b.WriteString(strings.Repeat("=", 60) + "\n\n")

	// Summary
	b.WriteString("SUMMARY\n")
	b.WriteString(strings.Repeat("-", 40) + "\n")
	b.WriteString(fmt.Sprintf("  Income:       $%10.2f\n", r.Summary.TotalIncome))
	b.WriteString(fmt.Sprintf("  Expenses:     $%10.2f\n", r.Summary.TotalExpenses))
	b.WriteString(fmt.Sprintf("  Net Savings:  $%10.2f\n", r.Summary.NetSavings))
	b.WriteString(fmt.Sprintf("  Savings Rate: %9.1f%%\n", r.Summary.SavingsRate))
	b.WriteString(fmt.Sprintf("  Transactions: %10d\n\n", r.Summary.TxnCount))

	// Category breakdown
	if len(r.ByCategory) > 0 {
		b.WriteString("SPENDING BY CATEGORY\n")
		b.WriteString(strings.Repeat("-", 60) + "\n")
		b.WriteString(fmt.Sprintf("  %-20s %10s %5s %10s %8s\n", "Category", "Total", "Cnt", "Avg", "vs Prev"))
		for _, c := range r.ByCategory {
			delta := ""
			if c.PrevMonth > 0 {
				if c.DeltaPct >= 0 {
					delta = fmt.Sprintf("+%.0f%%", c.DeltaPct)
				} else {
					delta = fmt.Sprintf("%.0f%%", c.DeltaPct)
				}
			}
			b.WriteString(fmt.Sprintf("  %-20s $%9.2f %5d $%9.2f %8s\n",
				truncate(c.Name, 20), c.Total, c.Count, c.AvgAmount, delta))
		}
		b.WriteString("\n")
	}

	// Top merchants
	if len(r.TopMerchants) > 0 {
		b.WriteString("TOP MERCHANTS\n")
		b.WriteString(strings.Repeat("-", 45) + "\n")
		for i, m := range r.TopMerchants {
			b.WriteString(fmt.Sprintf("  %2d. %-25s $%9.2f (%d)\n", i+1, truncate(m.Name, 25), m.Total, m.Count))
		}
		b.WriteString("\n")
	}

	// Budget performance
	if len(r.BudgetPerf) > 0 {
		b.WriteString("BUDGET PERFORMANCE\n")
		b.WriteString(strings.Repeat("-", 70) + "\n")
		for _, bp := range r.BudgetPerf {
			status := "OK"
			if bp.Pct >= 100 {
				status = "OVER"
			} else if bp.Pct >= 80 {
				status = "WARN"
			}
			b.WriteString(fmt.Sprintf("  %-20s $%.0f / $%.0f %s %4.0f%% %s\n",
				truncate(bp.RuleName, 20), bp.Spent, bp.Limit, bp.Bar, bp.Pct, status))
		}
		b.WriteString("\n")
	}

	// Account balances
	if len(r.Accounts) > 0 {
		b.WriteString("ACCOUNT BALANCES\n")
		b.WriteString(strings.Repeat("-", 45) + "\n")
		for _, a := range r.Accounts {
			b.WriteString(fmt.Sprintf("  %-25s (%s) $%.2f\n", a.Name, a.Type, a.Balance))
		}
		b.WriteString("\n")
	}

	// Uncategorized
	if len(r.Uncategorized) > 0 {
		b.WriteString(fmt.Sprintf("UNCATEGORIZED TRANSACTIONS (%d)\n", len(r.Uncategorized)))
		b.WriteString(strings.Repeat("-", 60) + "\n")
		for _, t := range r.Uncategorized {
			b.WriteString(fmt.Sprintf("  %s  %-30s $%.2f  [%s]\n",
				t.Date, truncate(t.Description, 30), t.Amount, t.ID[:8]))
		}
		b.WriteString("\n")
	}

	// Claude analysis
	if r.Analysis != "" {
		b.WriteString("AI ANALYSIS\n")
		b.WriteString(strings.Repeat("-", 60) + "\n")
		b.WriteString(r.Analysis)
		b.WriteString("\n")
	}

	return b.String()
}

// CategorySummaryForAI returns a condensed spending summary suitable for Claude analysis.
func (r *MonthlyReport) CategorySummaryForAI() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Month: %s", r.YearMonth))
	lines = append(lines, fmt.Sprintf("Income: $%.2f | Expenses: $%.2f | Savings: $%.2f (%.0f%%)",
		r.Summary.TotalIncome, r.Summary.TotalExpenses, r.Summary.NetSavings, r.Summary.SavingsRate))
	lines = append(lines, "")
	lines = append(lines, "Category spending:")
	for _, c := range r.ByCategory {
		delta := ""
		if c.PrevMonth > 0 {
			delta = fmt.Sprintf(" (prev month: $%.2f, change: %+.0f%%)", c.PrevMonth, c.DeltaPct)
		}
		lines = append(lines, fmt.Sprintf("  %s: $%.2f (%d txns)%s", c.Name, c.Total, c.Count, delta))
	}
	if len(r.TopMerchants) > 0 {
		lines = append(lines, "")
		lines = append(lines, "Top merchants:")
		for _, m := range r.TopMerchants {
			lines = append(lines, fmt.Sprintf("  %s: $%.2f (%d txns)", m.Name, m.Total, m.Count))
		}
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "~"
}

// SortCategoriesByTotal sorts category rows by total descending.
func SortCategoriesByTotal(cats []CategoryRow) {
	sort.Slice(cats, func(i, j int) bool {
		return cats[i].Total > cats[j].Total
	})
}
