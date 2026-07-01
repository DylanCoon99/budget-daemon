package reports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	claudeAPI   = "https://api.anthropic.com/v1/messages"
	claudeModel = "claude-sonnet-4-5-20250929"
)

// AnalyzeWithClaude sends the monthly spending summary to Claude for anomaly
// detection and savings suggestions. Returns the analysis text.
func AnalyzeWithClaude(ctx context.Context, apiKey string, report *MonthlyReport) (string, error) {
	if apiKey == "" {
		return "", nil
	}

	summary := report.CategorySummaryForAI()
	personalContext := loadPersonalContext()

	contextBlock := ""
	if personalContext != "" {
		contextBlock = fmt.Sprintf("\n\nPersonal context provided by the user (use this to avoid false conclusions):\n%s\n", personalContext)
	}

	userPrompt := fmt.Sprintf(`Here is my spending summary for %s:

%s%s

Based on this data:
1. Identify any category where spending is significantly above the previous month (flag anything >30%% increase).
2. Call out any transactions or patterns that look unusual.
3. Give me one specific, actionable suggestion to save money next month.

Keep your analysis concise — under 200 words. Be direct and specific, not generic.`, report.YearMonth, summary, contextBlock)

	reqBody := map[string]any{
		"model":      claudeModel,
		"max_tokens": 1024,
		"system":     "You are a concise personal finance analyst. Analyze spending data and give specific, actionable insights. No pleasantries or filler. Use dollar amounts and percentages.",
		"messages": []map[string]string{
			{"role": "user", "content": userPrompt},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", claudeAPI, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("claude analysis failed", "status", resp.StatusCode, "body", string(respBody))
		return "", fmt.Errorf("claude error (status %d)", resp.StatusCode)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(result.Content) == 0 {
		return "", nil
	}

	return result.Content[0].Text, nil
}

// loadPersonalContext reads ~/.budget/context.txt for user-provided context.
func loadPersonalContext() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(home, ".budget", "context.txt"))
	if err != nil {
		return ""
	}

	// Strip comment lines (starting with #)
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			lines = append(lines, trimmed)
		}
	}

	return strings.Join(lines, "\n")
}
