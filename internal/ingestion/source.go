package ingestion

import (
	"context"
	"time"
)

// RawTransaction represents a transaction from any ingestion source
// before categorization.
type RawTransaction struct {
	ExternalID  string
	AccountID   string
	Date        time.Time
	Amount      float64 // Negative = money out, positive = money in
	Description string
	Pending     bool
	Metadata    map[string]any
}

// Source is the interface that all ingestion backends implement.
type Source interface {
	Name() string
	Sync(ctx context.Context) ([]RawTransaction, error)
}
