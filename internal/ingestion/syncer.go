package ingestion

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/dylancoon/budget-daemon/internal/crypto"
	"github.com/google/uuid"
)

// Syncer orchestrates transaction syncing across all configured institutions.
type Syncer struct {
	db                   *sql.DB
	plaidClientID        string
	plaidSecret          string
	plaidEnv             string
	encryptionKey string
}

func NewSyncer(db *sql.DB, plaidClientID, plaidSecret, plaidEnv, encryptionKey string) *Syncer {
	return &Syncer{
		db:            db,
		plaidClientID: plaidClientID,
		plaidSecret:   plaidSecret,
		plaidEnv:      plaidEnv,
		encryptionKey: encryptionKey,
	}
}

// SyncAll syncs transactions from all configured institutions.
// Returns total new transactions inserted.
func (s *Syncer) SyncAll(ctx context.Context) (int, error) {
	institutions, err := s.loadInstitutions(ctx)
	if err != nil {
		return 0, fmt.Errorf("load institutions: %w", err)
	}

	if len(institutions) == 0 {
		fmt.Println("No institutions configured. Run 'budget setup' first.")
		return 0, nil
	}

	totalNew := 0
	for _, inst := range institutions {
		slog.Info("syncing institution", "name", inst.Name, "source", inst.SourceType)

		source, err := s.buildSource(ctx, inst)
		if err != nil {
			slog.Error("failed to build source", "institution", inst.Name, "error", err)
			continue
		}
		if source == nil {
			// Import-only source (e.g., CSV) — skip during automated sync
			continue
		}

		txns, err := source.Sync(ctx)
		if err != nil {
			slog.Error("sync failed", "institution", inst.Name, "error", err)
			continue
		}

		inserted, err := s.InsertTransactions(ctx, txns)
		if err != nil {
			slog.Error("insert failed", "institution", inst.Name, "error", err)
			continue
		}

		// Sync balances for Plaid institutions
		if inst.SourceType == "plaid" {
			s.syncPlaidBalances(ctx, inst)
		}

		// Update last synced timestamp
		s.db.ExecContext(ctx, `UPDATE institutions SET last_synced_at = ? WHERE id = ?`,
			time.Now().UTC(), inst.ID)

		totalNew += inserted
		slog.Info("sync complete", "institution", inst.Name, "new_transactions", inserted)
	}

	return totalNew, nil
}

// SyncInstitution syncs a single institution by name.
func (s *Syncer) SyncInstitution(ctx context.Context, name string) (int, error) {
	inst, err := s.findInstitution(ctx, name)
	if err != nil {
		return 0, err
	}

	source, err := s.buildSource(ctx, inst)
	if err != nil {
		return 0, fmt.Errorf("build source: %w", err)
	}
	if source == nil {
		return 0, fmt.Errorf("%s is configured for manual CSV import only — use 'budget import' instead", name)
	}

	txns, err := source.Sync(ctx)
	if err != nil {
		return 0, fmt.Errorf("sync: %w", err)
	}

	inserted, err := s.InsertTransactions(ctx, txns)
	if err != nil {
		return 0, fmt.Errorf("insert: %w", err)
	}

	s.db.ExecContext(ctx, `UPDATE institutions SET last_synced_at = ? WHERE id = ?`,
		time.Now().UTC(), inst.ID)

	return inserted, nil
}

type institution struct {
	ID          string
	Name        string
	SourceType  string
	AccessToken []byte // encrypted
	SyncCursor  string
	OFXURL      string
}

func (s *Syncer) loadInstitutions(ctx context.Context) ([]institution, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, source_type, access_token, COALESCE(sync_cursor, ''), COALESCE(ofx_url, '')
		FROM institutions
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []institution
	for rows.Next() {
		var inst institution
		if err := rows.Scan(&inst.ID, &inst.Name, &inst.SourceType, &inst.AccessToken, &inst.SyncCursor, &inst.OFXURL); err != nil {
			return nil, err
		}
		results = append(results, inst)
	}
	return results, nil
}

func (s *Syncer) findInstitution(ctx context.Context, name string) (institution, error) {
	var inst institution
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, source_type, access_token, COALESCE(sync_cursor, ''), COALESCE(ofx_url, '')
		FROM institutions WHERE LOWER(name) = LOWER(?)
		ORDER BY CASE source_type WHEN 'plaid' THEN 0 WHEN 'ofx' THEN 1 ELSE 2 END
		LIMIT 1
	`, name).Scan(&inst.ID, &inst.Name, &inst.SourceType, &inst.AccessToken, &inst.SyncCursor, &inst.OFXURL)
	if err == sql.ErrNoRows {
		return inst, fmt.Errorf("institution %q not found (run 'budget setup' to add it)", name)
	}
	return inst, err
}

func (s *Syncer) buildSource(ctx context.Context, inst institution) (Source, error) {
	switch inst.SourceType {
	case "plaid":
		accessToken, err := crypto.Decrypt(inst.AccessToken, s.encryptionKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt access token: %w", err)
		}

		return NewPlaidSource(
			s.plaidClientID,
			s.plaidSecret,
			s.plaidEnv,
			accessToken,
			inst.SyncCursor,
			func(newCursor string) error {
				_, err := s.db.ExecContext(ctx, `UPDATE institutions SET sync_cursor = ? WHERE id = ?`,
					newCursor, inst.ID)
				return err
			},
		), nil

	case "csv":
		// CSV institutions are import-only, not auto-synced
		return nil, nil

	case "ofx":
		// OFX credentials would need to be stored separately or prompted
		return nil, fmt.Errorf("OFX sync requires manual configuration — use 'budget import' with CSV for now")

	default:
		return nil, fmt.Errorf("unknown source type: %s", inst.SourceType)
	}
}

// InsertTransactions deduplicates and inserts raw transactions into the database.
func (s *Syncer) InsertTransactions(ctx context.Context, txns []RawTransaction) (int, error) {
	inserted := 0
	for _, txn := range txns {
		// Ensure the account exists in our database
		accountID, err := s.ensureAccount(ctx, txn.AccountID)
		if err != nil {
			slog.Warn("skipping transaction, account issue", "account", txn.AccountID, "error", err)
			continue
		}

		metadata, _ := json.Marshal(txn.Metadata)

		res, err := s.db.ExecContext(ctx, `
			INSERT INTO transactions (id, external_id, account_id, date, amount, description, is_pending, raw_metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (external_id) DO UPDATE SET
				is_pending = excluded.is_pending,
				updated_at = datetime('now')
		`, uuid.New().String(), txn.ExternalID, accountID, txn.Date.Format("2006-01-02"),
			txn.Amount, txn.Description, boolToInt(txn.Pending), string(metadata))
		if err != nil {
			slog.Warn("insert transaction failed", "external_id", txn.ExternalID, "error", err)
			continue
		}

		rowsAffected, _ := res.RowsAffected()
		if rowsAffected > 0 {
			inserted++
		}
	}
	return inserted, nil
}

// ensureAccount checks if the external account ID maps to an internal account.
// If not found, it returns the external ID as-is (it will be created during setup).
func (s *Syncer) ensureAccount(ctx context.Context, externalAccountID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE external_id = ?`, externalAccountID).Scan(&id)
	if err == sql.ErrNoRows {
		// Account not yet registered — use external ID directly
		// This happens when Plaid returns accounts we haven't seen before
		return externalAccountID, nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// RegisterPlaidInstitution saves a Plaid-connected institution and its accounts to the database.
func (s *Syncer) RegisterPlaidInstitution(ctx context.Context, name string, result *PlaidLinkResult) error {
	// Encrypt the access token
	encToken, err := crypto.Encrypt(result.AccessToken, s.encryptionKey)
	if err != nil {
		return fmt.Errorf("encrypt token: %w", err)
	}

	instID := uuid.New().String()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO institutions (id, name, source_type, plaid_item_id, access_token, created_at)
		VALUES (?, ?, 'plaid', ?, ?, datetime('now'))
	`, instID, name, result.ItemID, encToken)
	if err != nil {
		return fmt.Errorf("insert institution: %w", err)
	}

	// Register each account
	for _, acct := range result.Accounts {
		acctID := uuid.New().String()
		acctType := mapPlaidAccountType(acct.Type, acct.Subtype)

		_, err = s.db.ExecContext(ctx, `
			INSERT INTO accounts (id, institution_id, external_id, name, account_type, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		`, acctID, instID, acct.ID, acct.Name, acctType)
		if err != nil {
			slog.Warn("failed to register account", "name", acct.Name, "error", err)
			continue
		}
		slog.Info("registered account", "name", acct.Name, "type", acctType)
	}

	return nil
}

// RegisterCSVInstitution creates an institution and account for CSV-based imports.
func (s *Syncer) RegisterCSVInstitution(ctx context.Context, institutionName, accountName, accountType string) (string, error) {
	instID := uuid.New().String()
	acctID := uuid.New().String()
	externalID := fmt.Sprintf("csv_%s_%s", institutionName, accountName)

	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO institutions (id, name, source_type, created_at)
		VALUES (?, ?, 'csv', datetime('now'))
	`, instID, institutionName)
	if err != nil {
		return "", fmt.Errorf("insert institution: %w", err)
	}

	// Check if account already exists
	var existingID string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE external_id = ?`, externalID).Scan(&existingID)
	if err == nil {
		return existingID, nil // Already registered
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO accounts (id, institution_id, external_id, name, account_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))
	`, acctID, instID, externalID, accountName, accountType)
	if err != nil {
		return "", fmt.Errorf("insert account: %w", err)
	}

	return acctID, nil
}

// syncPlaidBalances fetches current balances from Plaid and updates the accounts table.
func (s *Syncer) syncPlaidBalances(ctx context.Context, inst institution) {
	accessToken, err := crypto.Decrypt(inst.AccessToken, s.encryptionKey)
	if err != nil {
		slog.Error("decrypt token for balance sync", "error", err)
		return
	}

	baseURL := "https://sandbox.plaid.com"
	switch s.plaidEnv {
	case "development":
		baseURL = "https://development.plaid.com"
	case "production":
		baseURL = "https://production.plaid.com"
	}

	payload := map[string]string{
		"client_id":    s.plaidClientID,
		"secret":       s.plaidSecret,
		"access_token": accessToken,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/accounts/get", bytes.NewReader(body))
	if err != nil {
		slog.Error("create balance request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("balance request failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		slog.Error("plaid balance error", "status", resp.StatusCode, "body", string(respBody[:min(200, len(respBody))]))
		return
	}

	var result struct {
		Accounts []struct {
			AccountID string `json:"account_id"`
			Balances  struct {
				Current   *float64 `json:"current"`
				Available *float64 `json:"available"`
			} `json:"balances"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("decode balance response", "error", err)
		return
	}

	for _, acct := range result.Accounts {
		var current, available float64
		if acct.Balances.Current != nil {
			current = *acct.Balances.Current
		}
		if acct.Balances.Available != nil {
			available = *acct.Balances.Available
		}

		_, err := s.db.ExecContext(ctx, `
			UPDATE accounts SET current_balance = ?, available_balance = ?, updated_at = datetime('now')
			WHERE external_id = ?
		`, current, available, acct.AccountID)
		if err != nil {
			slog.Warn("update balance failed", "account", acct.AccountID, "error", err)
		}
	}

	slog.Info("balances updated", "institution", inst.Name)
}

func mapPlaidAccountType(plaidType, plaidSubtype string) string {
	switch plaidType {
	case "depository":
		if plaidSubtype == "savings" {
			return "savings"
		}
		return "checking"
	case "credit":
		return "credit"
	case "investment":
		return "investment"
	default:
		return plaidType
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
