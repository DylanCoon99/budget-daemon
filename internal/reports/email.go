package reports

import (
	"fmt"
	"math"
	"strings"
)

// FormatHTML generates an HTML email body for the monthly report.
func (r *MonthlyReport) FormatHTML() string {
	var b strings.Builder

	b.WriteString(`<!DOCTYPE html>
<html>
<head>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; max-width: 640px; margin: 0 auto; padding: 20px; color: #333; background: #f9f9f9; }
  .card { background: white; border-radius: 8px; padding: 20px; margin-bottom: 16px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
  h1 { font-size: 24px; margin: 0 0 4px 0; }
  h2 { font-size: 16px; margin: 0 0 12px 0; color: #666; border-bottom: 1px solid #eee; padding-bottom: 8px; }
  .subtitle { color: #888; font-size: 14px; margin-bottom: 20px; }
  table { width: 100%; border-collapse: collapse; font-size: 14px; }
  th { text-align: left; padding: 6px 8px; color: #888; font-weight: 500; font-size: 12px; text-transform: uppercase; }
  td { padding: 6px 8px; border-top: 1px solid #f0f0f0; }
  .amount { text-align: right; font-variant-numeric: tabular-nums; }
  .positive { color: #16a34a; }
  .negative { color: #dc2626; }
  .neutral { color: #666; }
  .summary-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
  .summary-item { padding: 12px; background: #f5f5f5; border-radius: 6px; }
  .summary-label { font-size: 12px; color: #888; text-transform: uppercase; }
  .summary-value { font-size: 22px; font-weight: 600; margin-top: 2px; }
  .bar-container { display: inline-block; width: 100px; height: 8px; background: #e5e5e5; border-radius: 4px; vertical-align: middle; }
  .bar-fill { height: 100%; border-radius: 4px; }
  .bar-ok { background: #16a34a; }
  .bar-warn { background: #f59e0b; }
  .bar-over { background: #dc2626; }
  .delta-up { color: #dc2626; font-size: 12px; }
  .delta-down { color: #16a34a; font-size: 12px; }
  .analysis { background: #f0f7ff; border-left: 3px solid #3b82f6; padding: 12px 16px; border-radius: 0 6px 6px 0; line-height: 1.6; font-size: 14px; }
  .tag { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 11px; font-weight: 500; }
  .tag-ok { background: #dcfce7; color: #166534; }
  .tag-warn { background: #fef3c7; color: #92400e; }
  .tag-over { background: #fee2e2; color: #991b1b; }
</style>
</head>
<body>
`)

	// Header
	b.WriteString(fmt.Sprintf(`<div class="card">
  <h1>Budget Report</h1>
  <div class="subtitle">%s</div>
  <div class="summary-grid">
    <div class="summary-item">
      <div class="summary-label">Income</div>
      <div class="summary-value positive">$%.2f</div>
    </div>
    <div class="summary-item">
      <div class="summary-label">Expenses</div>
      <div class="summary-value negative">$%.2f</div>
    </div>
    <div class="summary-item">
      <div class="summary-label">Net Savings</div>
      <div class="summary-value %s">$%.2f</div>
    </div>
    <div class="summary-item">
      <div class="summary-label">Savings Rate</div>
      <div class="summary-value %s">%.1f%%</div>
    </div>
  </div>
</div>
`, r.YearMonth,
		r.Summary.TotalIncome,
		r.Summary.TotalExpenses,
		colorClass(r.Summary.NetSavings), r.Summary.NetSavings,
		colorClass(r.Summary.SavingsRate), r.Summary.SavingsRate))

	// Category breakdown
	if len(r.ByCategory) > 0 {
		b.WriteString(`<div class="card">
  <h2>Spending by Category</h2>
  <table>
    <tr><th>Category</th><th class="amount">Total</th><th class="amount">Count</th><th class="amount">vs Prev</th></tr>
`)
		for _, c := range r.ByCategory {
			delta := ""
			if c.PrevMonth > 0 {
				cls := "delta-down"
				sign := ""
				if c.DeltaPct >= 0 {
					cls = "delta-up"
					sign = "+"
				}
				delta = fmt.Sprintf(`<span class="%s">%s%.0f%%</span>`, cls, sign, c.DeltaPct)
			}
			b.WriteString(fmt.Sprintf(`    <tr><td>%s</td><td class="amount">$%.2f</td><td class="amount">%d</td><td class="amount">%s</td></tr>
`, c.Name, c.Total, c.Count, delta))
		}
		b.WriteString("  </table>\n</div>\n")
	}

	// Top merchants
	if len(r.TopMerchants) > 0 {
		b.WriteString(`<div class="card">
  <h2>Top Merchants</h2>
  <table>
    <tr><th>#</th><th>Merchant</th><th class="amount">Total</th><th class="amount">Txns</th></tr>
`)
		for i, m := range r.TopMerchants {
			b.WriteString(fmt.Sprintf(`    <tr><td>%d</td><td>%s</td><td class="amount">$%.2f</td><td class="amount">%d</td></tr>
`, i+1, m.Name, m.Total, m.Count))
		}
		b.WriteString("  </table>\n</div>\n")
	}

	// Budget performance
	if len(r.BudgetPerf) > 0 {
		b.WriteString(`<div class="card">
  <h2>Budget Performance</h2>
  <table>
    <tr><th>Budget</th><th class="amount">Spent</th><th class="amount">Limit</th><th>Progress</th><th>Status</th></tr>
`)
		for _, bp := range r.BudgetPerf {
			barCls := "bar-ok"
			tagCls := "tag-ok"
			tagText := "OK"
			if bp.Pct >= 100 {
				barCls = "bar-over"
				tagCls = "tag-over"
				tagText = "OVER"
			} else if bp.Pct >= 80 {
				barCls = "bar-warn"
				tagCls = "tag-warn"
				tagText = "WARN"
			}
			fillWidth := math.Min(bp.Pct, 100)

			b.WriteString(fmt.Sprintf(`    <tr>
      <td>%s</td>
      <td class="amount">$%.0f</td>
      <td class="amount">$%.0f</td>
      <td><div class="bar-container"><div class="bar-fill %s" style="width:%.0f%%"></div></div> %.0f%%</td>
      <td><span class="tag %s">%s</span></td>
    </tr>
`, bp.RuleName, bp.Spent, bp.Limit, barCls, fillWidth, bp.Pct, tagCls, tagText))
		}
		b.WriteString("  </table>\n</div>\n")
	}

	// AI Analysis
	if r.Analysis != "" {
		b.WriteString(fmt.Sprintf(`<div class="card">
  <h2>AI Analysis</h2>
  <div class="analysis">%s</div>
</div>
`, strings.ReplaceAll(r.Analysis, "\n", "<br>")))
	}

	// Uncategorized
	if len(r.Uncategorized) > 0 {
		b.WriteString(fmt.Sprintf(`<div class="card">
  <h2>Needs Review (%d uncategorized)</h2>
  <table>
    <tr><th>Date</th><th>Description</th><th class="amount">Amount</th></tr>
`, len(r.Uncategorized)))
		for _, t := range r.Uncategorized {
			b.WriteString(fmt.Sprintf(`    <tr><td>%s</td><td>%s</td><td class="amount">$%.2f</td></tr>
`, t.Date, t.Description, t.Amount))
		}
		b.WriteString("  </table>\n</div>\n")
	}

	b.WriteString(`<div style="text-align:center; color:#aaa; font-size:12px; margin-top:20px;">
  Generated by Budget Daemon
</div>
</body>
</html>`)

	return b.String()
}

func colorClass(val float64) string {
	if val > 0 {
		return "positive"
	}
	if val < 0 {
		return "negative"
	}
	return "neutral"
}
