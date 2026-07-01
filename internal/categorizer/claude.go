package categorizer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const (
	claudeAPI        = "https://api.anthropic.com/v1/messages"
	claudeModel      = "claude-sonnet-4-5-20250929"
	maxBatchSize     = 50
)

// ClaudeClient handles Tier 3 categorization via the Claude API.
type ClaudeClient struct {
	apiKey string
	db     *sql.DB
}

func NewClaudeClient(apiKey string, db *sql.DB) *ClaudeClient {
	return &ClaudeClient{apiKey: apiKey, db: db}
}

// CategorizeBatch sends uncategorized transactions to Claude in batches of up to 50.
func (c *ClaudeClient) CategorizeBatch(ctx context.Context, txns []uncategorizedTxn) ([]Result, error) {
	if c.apiKey == "" {
		slog.Warn("CLAUDE_API_KEY not set, skipping tier 3 categorization")
		return nil, nil
	}

	// Get the list of valid categories
	categories, err := c.loadCategories()
	if err != nil {
		return nil, fmt.Errorf("load categories: %w", err)
	}

	var allResults []Result

	// Process in batches
	for i := 0; i < len(txns); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(txns) {
			end = len(txns)
		}
		batch := txns[i:end]

		results, err := c.categorizeSingle(ctx, batch, categories)
		if err != nil {
			return allResults, fmt.Errorf("batch %d-%d: %w", i, end, err)
		}
		allResults = append(allResults, results...)
	}

	return allResults, nil
}

func (c *ClaudeClient) categorizeSingle(ctx context.Context, txns []uncategorizedTxn, categories map[string]int) ([]Result, error) {
	// Build the prompt
	var catNames []string
	for name := range categories {
		catNames = append(catNames, name)
	}

	var txnLines []string
	for i, t := range txns {
		txnLines = append(txnLines, fmt.Sprintf("%d: %q $%.2f (%s)", i, t.Description, t.Amount, t.Date))
	}

	userPrompt := fmt.Sprintf(`Categorize these transactions. For each, return the index, category name, cleaned merchant name, and confidence (0.0-1.0).

Categories: [%s]

Transactions:
%s

Respond ONLY with a JSON array, no other text:
[{"i":0,"cat":"Category Name","merchant":"Clean Name","conf":0.95}, ...]`,
		strings.Join(catNames, ", "),
		strings.Join(txnLines, "\n"),
	)

	// Call Claude API
	reqBody := claudeRequest{
		Model:     claudeModel,
		MaxTokens: 4096,
		System:    "You are a personal finance categorization engine. Given a list of transactions, assign each one a category from the provided list and extract a clean merchant name. Respond in JSON only. Do not include markdown formatting or code fences.",
		Messages: []claudeMessage{
			{Role: "user", Content: userPrompt},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", claudeAPI, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claude error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse Claude response
	var claudeResp claudeResponse
	if err := json.Unmarshal(respBody, &claudeResp); err != nil {
		return nil, fmt.Errorf("decode claude response: %w", err)
	}

	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("empty claude response")
	}

	// Extract the text content and parse as JSON
	text := claudeResp.Content[0].Text
	// Strip markdown code fences if present
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var items []claudeCategoryItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		slog.Error("failed to parse claude categorization", "response", text, "error", err)
		return nil, fmt.Errorf("parse categorization json: %w", err)
	}

	// Map results back to transaction IDs
	var results []Result
	for _, item := range items {
		if item.Index < 0 || item.Index >= len(txns) {
			continue
		}

		catID, ok := categories[item.Category]
		if !ok {
			// Try case-insensitive match
			for name, id := range categories {
				if strings.EqualFold(name, item.Category) {
					catID = id
					ok = true
					item.Category = name
					break
				}
			}
		}
		if !ok {
			slog.Warn("claude returned unknown category", "category", item.Category)
			continue
		}

		results = append(results, Result{
			TransactionID: txns[item.Index].ID,
			CategoryID:    catID,
			CategoryName:  item.Category,
			MerchantName:  item.Merchant,
			Confidence:    item.Confidence,
			Tier:          3,
		})
	}

	slog.Info("claude categorized", "count", len(results), "batch_size", len(txns))
	return results, nil
}

func (c *ClaudeClient) loadCategories() (map[string]int, error) {
	rows, err := c.db.Query(`SELECT id, name FROM categories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cats := make(map[string]int)
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		cats[name] = id
	}
	return cats, nil
}

// Claude API types

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []claudeContent `json:"content"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeCategoryItem struct {
	Index      int     `json:"i"`
	Category   string  `json:"cat"`
	Merchant   string  `json:"merchant"`
	Confidence float64 `json:"conf"`
}
