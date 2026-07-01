package categorizer

import (
	"context"
	"database/sql"
	"strings"
)

// OverrideMatcher checks the categorization_overrides table for learned patterns.
// This is Tier 1 — highest priority, zero cost, instant.
type OverrideMatcher struct {
	db *sql.DB
}

type OverrideResult struct {
	CategoryID   int
	CategoryName string
	MerchantName string
}

func NewOverrideMatcher(db *sql.DB) *OverrideMatcher {
	return &OverrideMatcher{db: db}
}

// Match checks if the transaction description matches any override pattern.
// Patterns are matched case-insensitively as substrings.
func (o *OverrideMatcher) Match(ctx context.Context, description string) (OverrideResult, bool) {
	descLower := strings.ToLower(description)

	rows, err := o.db.QueryContext(ctx, `
		SELECT co.description_pattern, co.category_id, c.name
		FROM categorization_overrides co
		JOIN categories c ON c.id = co.category_id
		WHERE co.is_regex = 0
		ORDER BY co.priority DESC
	`)
	if err != nil {
		return OverrideResult{}, false
	}
	defer rows.Close()

	for rows.Next() {
		var pattern string
		var categoryID int
		var categoryName string
		if err := rows.Scan(&pattern, &categoryID, &categoryName); err != nil {
			continue
		}

		if strings.Contains(descLower, strings.ToLower(pattern)) {
			return OverrideResult{
				CategoryID:   categoryID,
				CategoryName: categoryName,
				MerchantName: cleanMerchant(description),
			}, true
		}
	}

	return OverrideResult{}, false
}

// cleanMerchant extracts a readable merchant name from a raw transaction description.
// e.g., "WHOLEFDS MKT 10432 PITTSBURGH PA" -> "Wholefds Mkt"
func cleanMerchant(desc string) string {
	// Remove trailing location info (city/state patterns, store numbers)
	parts := strings.Fields(desc)
	if len(parts) <= 2 {
		return strings.Title(strings.ToLower(desc))
	}

	// Keep the first 2-3 meaningful words, drop numbers and state abbreviations
	var cleaned []string
	for _, p := range parts {
		// Stop at pure numbers or 2-letter state codes at the end
		if isStoreNumber(p) || isStateCode(p) {
			break
		}
		// Stop at common suffixes like "#1234"
		if strings.HasPrefix(p, "#") {
			break
		}
		cleaned = append(cleaned, p)
		if len(cleaned) >= 3 {
			break
		}
	}

	if len(cleaned) == 0 {
		return desc
	}

	return strings.Title(strings.ToLower(strings.Join(cleaned, " ")))
}

func isStoreNumber(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) >= 3
}

func isStateCode(s string) bool {
	if len(s) != 2 {
		return false
	}
	s = strings.ToUpper(s)
	states := []string{
		"AL", "AK", "AZ", "AR", "CA", "CO", "CT", "DE", "FL", "GA",
		"HI", "ID", "IL", "IN", "IA", "KS", "KY", "LA", "ME", "MD",
		"MA", "MI", "MN", "MS", "MO", "MT", "NE", "NV", "NH", "NJ",
		"NM", "NY", "NC", "ND", "OH", "OK", "OR", "PA", "RI", "SC",
		"SD", "TN", "TX", "UT", "VT", "VA", "WA", "WV", "WI", "WY",
	}
	for _, st := range states {
		if s == st {
			return true
		}
	}
	return false
}
