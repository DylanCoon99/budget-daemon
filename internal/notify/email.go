package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// SendGridEmail sends email notifications via the SendGrid v3 API.
type SendGridEmail struct {
	apiKey  string
	from    string // Sender email address
	to      string // Recipient email address
}

func NewSendGridEmail(apiKey, from, to string) *SendGridEmail {
	return &SendGridEmail{
		apiKey: apiKey,
		from:   from,
		to:     to,
	}
}

func (s *SendGridEmail) Name() string { return "sendgrid_email" }

func (s *SendGridEmail) Send(ctx context.Context, alert Alert) error {
	if s.apiKey == "" {
		slog.Warn("sendgrid not configured, skipping email")
		return nil
	}

	body := alert.Body
	if body == "" {
		body = alert.Subject
	}

	payload := sgMailPayload{
		Personalizations: []sgPersonalization{
			{To: []sgAddress{{Email: s.to}}},
		},
		From:    sgAddress{Email: s.from},
		Subject: alert.Subject,
		Content: []sgContent{
			{Type: "text/plain", Value: body},
		},
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal email: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.sendgrid.com/v3/mail/send", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendgrid request: %w", err)
	}
	defer resp.Body.Close()

	// SendGrid returns 202 on success
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendgrid error (status %d): %s", resp.StatusCode, string(respBody))
	}

	slog.Info("email sent", "to", s.to, "subject", alert.Subject)
	return nil
}

type sgMailPayload struct {
	Personalizations []sgPersonalization `json:"personalizations"`
	From             sgAddress           `json:"from"`
	Subject          string              `json:"subject"`
	Content          []sgContent         `json:"content"`
}

type sgPersonalization struct {
	To []sgAddress `json:"to"`
}

type sgAddress struct {
	Email string `json:"email"`
}

type sgContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
