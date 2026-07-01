package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// CSVSource imports transactions from a CSV file.
// Expected columns: Date, Description, Amount
// This is the manual fallback for any institution.
type CSVSource struct {
	FilePath  string
	AccountID string
}

func (c *CSVSource) Name() string { return "csv" }

func (c *CSVSource) Sync(ctx context.Context) ([]RawTransaction, error) {
	f, err := os.Open(c.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("csv has no data rows")
	}

	header := records[0]
	dateCol, descCol, amtCol := findColumns(header)
	if dateCol < 0 || descCol < 0 || amtCol < 0 {
		return nil, fmt.Errorf("csv must have Date, Description, and Amount columns (found: %v)", header)
	}

	var txns []RawTransaction
	for _, row := range records[1:] {
		if len(row) <= max(dateCol, descCol, amtCol) {
			continue
		}

		date, err := parseFlexDate(row[dateCol])
		if err != nil {
			continue
		}

		amount, err := parseAmount(row[amtCol])
		if err != nil {
			continue
		}

		desc := strings.TrimSpace(row[descCol])
		extID := generateExternalID(c.AccountID, date, amount, desc)

		txns = append(txns, RawTransaction{
			ExternalID:  extID,
			AccountID:   c.AccountID,
			Date:        date,
			Amount:      amount,
			Description: desc,
		})
	}

	return txns, nil
}

func findColumns(header []string) (dateCol, descCol, amtCol int) {
	dateCol, descCol, amtCol = -1, -1, -1
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		switch {
		case strings.Contains(h, "date"):
			dateCol = i
		case strings.Contains(h, "desc") || strings.Contains(h, "memo") || strings.Contains(h, "name"):
			descCol = i
		case strings.Contains(h, "amount") || strings.Contains(h, "amt"):
			amtCol = i
		}
	}
	return
}

func parseFlexDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	formats := []string{
		"2006-01-02",
		"01/02/2006",
		"1/2/2006",
		"01-02-2006",
		"Jan 2, 2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date format: %s", s)
}

func parseAmount(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "(", "-")
	s = strings.ReplaceAll(s, ")", "")
	return strconv.ParseFloat(s, 64)
}

func generateExternalID(accountID string, date time.Time, amount float64, desc string) string {
	data := fmt.Sprintf("%s|%s|%.2f|%s", accountID, date.Format("2006-01-02"), amount, desc)
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("csv_%x", hash[:12])
}

func max(a, b, c int) int {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}
