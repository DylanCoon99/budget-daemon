package categorizer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// Result holds the outcome of categorizing a single transaction.
type Result struct {
	TransactionID string
	CategoryID    int
	CategoryName  string
	MerchantName  string
	Confidence    float64
	Tier          int // 1=override, 2=keyword, 3=claude
}

// Pipeline runs transactions through three categorization tiers in order.
type Pipeline struct {
	db       *sql.DB
	override *OverrideMatcher
	keyword  *KeywordMatcher
	claude   *ClaudeClient
}

func NewPipeline(db *sql.DB, claudeAPIKey string) *Pipeline {
	return &Pipeline{
		db:       db,
		override: NewOverrideMatcher(db),
		keyword:  NewKeywordMatcher(db),
		claude:   NewClaudeClient(claudeAPIKey, db),
	}
}

// CategorizeAll finds all uncategorized transactions and runs them through the pipeline.
// Returns the number of transactions categorized.
func (p *Pipeline) CategorizeAll(ctx context.Context) (int, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, description, amount, date
		FROM transactions
		WHERE category_id IS NULL AND user_overridden = 0
		ORDER BY date DESC
	`)
	if err != nil {
		return 0, fmt.Errorf("query uncategorized: %w", err)
	}
	defer rows.Close()

	var pending []uncategorizedTxn
	for rows.Next() {
		var t uncategorizedTxn
		if err := rows.Scan(&t.ID, &t.Description, &t.Amount, &t.Date); err != nil {
			return 0, fmt.Errorf("scan transaction: %w", err)
		}
		pending = append(pending, t)
	}

	if len(pending) == 0 {
		return 0, nil
	}

	slog.Info("categorizing transactions", "count", len(pending))

	var needsClaude []uncategorizedTxn
	categorized := 0

	// Tier 1: Check override table
	for _, t := range pending {
		if result, ok := p.override.Match(ctx, t.Description); ok {
			if err := p.applyResult(ctx, t.ID, result.CategoryID, result.MerchantName, 1.0, 1); err != nil {
				return categorized, err
			}
			categorized++
			slog.Debug("tier 1 match", "txn", t.Description, "category", result.CategoryName)
			continue
		}

		// Tier 2: Keyword matching
		if result, ok := p.keyword.Match(t.Description); ok {
			if err := p.applyResult(ctx, t.ID, result.CategoryID, result.MerchantName, 0.9, 2); err != nil {
				return categorized, err
			}
			categorized++
			slog.Debug("tier 2 match", "txn", t.Description, "category", result.CategoryName)
			continue
		}

		needsClaude = append(needsClaude, t)
	}

	slog.Info("tier 1+2 results",
		"matched", categorized,
		"remaining", len(needsClaude),
	)

	// Tier 3: Claude API (batch)
	if len(needsClaude) > 0 {
		claudeResults, err := p.claude.CategorizeBatch(ctx, needsClaude)
		if err != nil {
			slog.Error("claude categorization failed", "error", err)
			// Don't fail the whole pipeline — just leave these uncategorized
			return categorized, nil
		}

		for _, r := range claudeResults {
			if err := p.applyResult(ctx, r.TransactionID, r.CategoryID, r.MerchantName, r.Confidence, 3); err != nil {
				return categorized, err
			}
			categorized++
		}
	}

	return categorized, nil
}

func (p *Pipeline) applyResult(ctx context.Context, txnID string, categoryID int, merchantName string, confidence float64, tier int) error {
	_, err := p.db.ExecContext(ctx, `
		UPDATE transactions
		SET category_id = ?, merchant_name = ?, ai_category_confidence = ?, ai_categorized_at = ?, updated_at = ?
		WHERE id = ?
	`, categoryID, merchantName, confidence, time.Now().UTC(), time.Now().UTC(), txnID)
	if err != nil {
		return fmt.Errorf("update transaction %s: %w", txnID, err)
	}
	return nil
}

// Recategorize lets the user override a transaction's category and learns from it.
func (p *Pipeline) Recategorize(ctx context.Context, txnID string, categoryName string) error {
	// Look up the category
	var categoryID int
	err := p.db.QueryRowContext(ctx, `SELECT id FROM categories WHERE name = ?`, categoryName).Scan(&categoryID)
	if err != nil {
		return fmt.Errorf("unknown category %q: %w", categoryName, err)
	}

	// Get the transaction description
	var description string
	err = p.db.QueryRowContext(ctx, `SELECT description FROM transactions WHERE id = ?`, txnID).Scan(&description)
	if err != nil {
		return fmt.Errorf("transaction %q not found: %w", txnID, err)
	}

	// Update the transaction
	_, err = p.db.ExecContext(ctx, `
		UPDATE transactions
		SET category_id = ?, user_overridden = 1, updated_at = ?
		WHERE id = ?
	`, categoryID, time.Now().UTC(), txnID)
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}

	// Learn: add an override so future identical descriptions auto-categorize
	_, err = p.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO categorization_overrides (description_pattern, category_id, priority)
		VALUES (?, ?, 10)
	`, description, categoryID)
	if err != nil {
		return fmt.Errorf("save override: %w", err)
	}

	slog.Info("recategorized", "txn", txnID, "category", categoryName, "learned_pattern", description)
	return nil
}

type uncategorizedTxn struct {
	ID          string
	Description string
	Amount      float64
	Date        string
}
