package notify

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// TwilioSMS sends SMS notifications via the Twilio REST API.
type TwilioSMS struct {
	accountSID string
	authToken  string
	from       string
	to         string
}

func NewTwilioSMS(accountSID, authToken, from, to string) *TwilioSMS {
	return &TwilioSMS{
		accountSID: accountSID,
		authToken:  authToken,
		from:       from,
		to:         to,
	}
}

func (t *TwilioSMS) Name() string { return "twilio_sms" }

func (t *TwilioSMS) Send(ctx context.Context, alert Alert) error {
	if t.accountSID == "" || t.authToken == "" {
		slog.Warn("twilio not configured, skipping SMS")
		return nil
	}

	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", t.accountSID)

	data := url.Values{}
	data.Set("To", t.to)
	data.Set("From", t.from)
	data.Set("Body", alert.Subject)

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(t.accountSID, t.authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("twilio request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("twilio error (status %d): %s", resp.StatusCode, string(body))
	}

	slog.Info("SMS sent", "to", t.to, "subject", alert.Subject)
	return nil
}
