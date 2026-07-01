package ingestion

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OFXSource fetches transactions from a bank that supports OFX Direct Connect.
// This is the primary fallback for CNB Bank if Plaid doesn't support it.
type OFXSource struct {
	OFXURL    string // Bank's OFX server URL
	OFXOrg    string // Organization name (from ofxhome.com)
	OFXFID    string // Financial Institution ID
	UserID    string // Online banking username
	Password  string // Online banking password
	AccountID string // Internal account ID in our database
	BankAcctID string // Bank's account number
	AcctType  string // "CHECKING", "SAVINGS", "CREDITCARD"
}

func (o *OFXSource) Name() string { return "ofx" }

func (o *OFXSource) Sync(ctx context.Context) ([]RawTransaction, error) {
	// Build the OFX request
	now := time.Now()
	startDate := now.AddDate(0, -1, 0) // Look back 30 days

	ofxRequest := o.buildRequest(startDate, now)

	req, err := http.NewRequestWithContext(ctx, "POST", o.OFXURL, strings.NewReader(ofxRequest))
	if err != nil {
		return nil, fmt.Errorf("create OFX request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ofx")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OFX request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read OFX response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OFX error (status %d): %s", resp.StatusCode, string(body[:min(500, len(body))]))
	}

	return o.parseResponse(string(body))
}

func (o *OFXSource) buildRequest(start, end time.Time) string {
	dtStart := start.Format("20060102120000")
	dtEnd := end.Format("20060102120000")
	dtNow := time.Now().Format("20060102150405")

	// OFX 1.x SGML format (most widely supported by regional banks)
	return fmt.Sprintf(`OFXHEADER:100
DATA:OFXSGML
VERSION:102
SECURITY:NONE
ENCODING:USASCII
CHARSET:1252
COMPRESSION:NONE
OLDFILEUID:NONE
NEWFILEUID:NONE

<OFX>
<SIGNONMSGSRQV1>
<SONRQ>
<DTCLIENT>%s
<USERID>%s
<USERPASS>%s
<LANGUAGE>ENG
<FI>
<ORG>%s
<FID>%s
</FI>
<APPID>Budget
<APPVER>0100
</SONRQ>
</SIGNONMSGSRQV1>
<BANKMSGSRQV1>
<STMTTRNRQ>
<TRNUID>%s
<STMTRQ>
<BANKACCTFROM>
<BANKID>%s
<ACCTID>%s
<ACCTTYPE>%s
</BANKACCTFROM>
<INCTRAN>
<DTSTART>%s
<DTEND>%s
<INCLUDE>Y
</INCTRAN>
</STMTRQ>
</STMTTRNRQ>
</BANKMSGSRQV1>
</OFX>`,
		dtNow, o.UserID, o.Password, o.OFXOrg, o.OFXFID,
		dtNow, o.OFXFID, o.BankAcctID, o.AcctType,
		dtStart, dtEnd)
}

func (o *OFXSource) parseResponse(body string) ([]RawTransaction, error) {
	// Parse OFX SGML — extract <STMTTRN> blocks
	var txns []RawTransaction

	remaining := body
	for {
		start := strings.Index(remaining, "<STMTTRN>")
		if start < 0 {
			break
		}
		end := strings.Index(remaining[start:], "</STMTTRN>")
		if end < 0 {
			break
		}

		block := remaining[start : start+end+len("</STMTTRN>")]
		remaining = remaining[start+end+len("</STMTTRN>"):]

		txn, err := o.parseTransaction(block)
		if err != nil {
			slog.Warn("skipping unparseable OFX transaction", "error", err)
			continue
		}
		txns = append(txns, txn)
	}

	slog.Info("parsed OFX transactions", "count", len(txns))
	return txns, nil
}

func (o *OFXSource) parseTransaction(block string) (RawTransaction, error) {
	fitID := extractOFXField(block, "FITID")
	dtPosted := extractOFXField(block, "DTPOSTED")
	trnAmt := extractOFXField(block, "TRNAMT")
	name := extractOFXField(block, "NAME")
	memo := extractOFXField(block, "MEMO")

	if fitID == "" || dtPosted == "" || trnAmt == "" {
		return RawTransaction{}, fmt.Errorf("missing required OFX fields")
	}

	date, err := parseOFXDate(dtPosted)
	if err != nil {
		return RawTransaction{}, fmt.Errorf("parse date %q: %w", dtPosted, err)
	}

	amount, err := parseAmount(trnAmt)
	if err != nil {
		return RawTransaction{}, fmt.Errorf("parse amount %q: %w", trnAmt, err)
	}

	description := name
	if description == "" {
		description = memo
	}
	if description == "" {
		description = "Unknown"
	}

	// Generate a stable external ID from the FITID
	extID := fmt.Sprintf("ofx_%s", fitID)
	if fitID == "" {
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%.2f|%s", o.AccountID, date.Format("2006-01-02"), amount, description)))
		extID = fmt.Sprintf("ofx_%x", hash[:12])
	}

	return RawTransaction{
		ExternalID:  extID,
		AccountID:   o.AccountID,
		Date:        date,
		Amount:      amount,
		Description: description,
		Metadata:    map[string]any{"memo": memo, "fitid": fitID, "source": "ofx"},
	}, nil
}

func extractOFXField(block, field string) string {
	tag := "<" + field + ">"
	start := strings.Index(block, tag)
	if start < 0 {
		return ""
	}
	start += len(tag)

	// Value runs until next < or newline
	end := start
	for end < len(block) && block[end] != '<' && block[end] != '\n' && block[end] != '\r' {
		end++
	}

	return strings.TrimSpace(block[start:end])
}

func parseOFXDate(s string) (time.Time, error) {
	// OFX dates: "20260701120000" or "20260701"
	if len(s) >= 14 {
		return time.Parse("20060102150405", s[:14])
	}
	if len(s) >= 8 {
		return time.Parse("20060102", s[:8])
	}
	return time.Time{}, fmt.Errorf("unrecognized OFX date: %s", s)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
