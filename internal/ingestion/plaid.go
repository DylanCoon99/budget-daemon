package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PlaidSource syncs transactions from a Plaid-connected institution.
type PlaidSource struct {
	clientID    string
	secret      string
	baseURL     string // "https://sandbox.plaid.com", "https://development.plaid.com", "https://production.plaid.com"
	accessToken string
	cursor      string // persisted sync cursor

	// Called after successful sync to persist the new cursor
	OnCursorUpdate func(newCursor string) error
}

func NewPlaidSource(clientID, secret, env, accessToken, cursor string, onCursorUpdate func(string) error) *PlaidSource {
	base := "https://sandbox.plaid.com"
	switch env {
	case "development":
		base = "https://development.plaid.com"
	case "production":
		base = "https://production.plaid.com"
	}
	return &PlaidSource{
		clientID:       clientID,
		secret:         secret,
		baseURL:        base,
		accessToken:    accessToken,
		cursor:         cursor,
		OnCursorUpdate: onCursorUpdate,
	}
}

func (p *PlaidSource) Name() string { return "plaid" }

func (p *PlaidSource) Sync(ctx context.Context) ([]RawTransaction, error) {
	var all []RawTransaction
	hasMore := true

	for hasMore {
		resp, err := p.syncPage(ctx)
		if err != nil {
			return nil, err
		}

		for _, t := range resp.Added {
			all = append(all, RawTransaction{
				ExternalID:  t.TransactionID,
				AccountID:   t.AccountID,
				Date:        parseDate(t.Date),
				Amount:      -t.Amount, // Plaid uses positive = debit, we flip
				Description: t.Name,
				Pending:     t.Pending,
				Metadata:    map[string]any{"plaid_category": t.Category, "merchant_name": t.MerchantName},
			})
		}

		p.cursor = resp.NextCursor
		hasMore = resp.HasMore

		if p.OnCursorUpdate != nil {
			if err := p.OnCursorUpdate(resp.NextCursor); err != nil {
				return nil, fmt.Errorf("persist cursor: %w", err)
			}
		}
	}

	return all, nil
}

type plaidSyncRequest struct {
	ClientID    string `json:"client_id"`
	Secret      string `json:"secret"`
	AccessToken string `json:"access_token"`
	Cursor      string `json:"cursor,omitempty"`
}

type plaidSyncResponse struct {
	Added      []plaidTransaction `json:"added"`
	Modified   []plaidTransaction `json:"modified"`
	Removed    []plaidRemoved     `json:"removed"`
	NextCursor string             `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
}

type plaidTransaction struct {
	TransactionID string   `json:"transaction_id"`
	AccountID     string   `json:"account_id"`
	Date          string   `json:"date"`
	Amount        float64  `json:"amount"`
	Name          string   `json:"name"`
	MerchantName  string   `json:"merchant_name"`
	Pending       bool     `json:"pending"`
	Category      []string `json:"category"`
}

type plaidRemoved struct {
	TransactionID string `json:"transaction_id"`
}

func (p *PlaidSource) syncPage(ctx context.Context) (*plaidSyncResponse, error) {
	body, _ := json.Marshal(plaidSyncRequest{
		ClientID:    p.clientID,
		Secret:      p.secret,
		AccessToken: p.accessToken,
		Cursor:      p.cursor,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/transactions/sync", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plaid request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plaid error (status %d): %s", resp.StatusCode, string(b))
	}

	var result plaidSyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode plaid response: %w", err)
	}

	return &result, nil
}

func parseDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}
