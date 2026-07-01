package ingestion

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// PlaidSetup handles the interactive Plaid Link flow for connecting bank accounts.
// It starts a local HTTP server, opens the browser to Plaid Link, and waits for
// the user to authenticate. Once complete, it exchanges the public_token for an
// access_token.
type PlaidSetup struct {
	ClientID string
	Secret   string
	BaseURL  string // e.g., "https://sandbox.plaid.com"
}

type PlaidLinkResult struct {
	AccessToken   string
	ItemID        string
	InstitutionID string
	Accounts      []PlaidAccount
}

type PlaidAccount struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`    // "depository", "credit", "investment"
	Subtype string `json:"subtype"` // "checking", "savings", "credit card"
}

// RunLinkFlow starts a local server on port 8477, opens the browser, and waits
// for the user to complete Plaid Link. Returns the access_token and account details.
func (ps *PlaidSetup) RunLinkFlow(ctx context.Context) (*PlaidLinkResult, error) {
	// Step 1: Create a link token
	linkToken, err := ps.createLinkToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("create link token: %w", err)
	}

	slog.Info("link token created", "token", linkToken[:20]+"...")

	// Step 2: Serve the Plaid Link page and wait for callback
	resultCh := make(chan linkCallback, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, plaidLinkHTML, linkToken)
	})
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var cb linkCallback
		if err := json.NewDecoder(r.Body).Decode(&cb); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h2>Connected! You can close this tab.</h2></body></html>`)
		resultCh <- cb
	})

	// Generate a self-signed TLS cert — Plaid Link in production
	// requires HTTPS, even on localhost.
	tlsCert, err := generateSelfSignedCert()
	if err != nil {
		return nil, fmt.Errorf("generate TLS cert: %w", err)
	}

	server := &http.Server{
		Addr:    "127.0.0.1:8477",
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		},
	}

	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	tlsLn := tls.NewListener(ln, server.TLSConfig)

	go func() {
		if err := server.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Open browser
	url := "https://127.0.0.1:8477"
	fmt.Printf("\nOpening browser to %s\n", url)
	fmt.Println("Your browser will warn about the self-signed certificate — click 'Advanced' then 'Proceed' to continue.")
	fmt.Println("Waiting for you to connect your bank account...\n")
	openBrowser(url)

	// Wait for callback or timeout
	var cb linkCallback
	select {
	case cb = <-resultCh:
	case err := <-errCh:
		server.Close()
		return nil, err
	case <-time.After(5 * time.Minute):
		server.Close()
		return nil, fmt.Errorf("timed out waiting for Plaid Link (5 minutes)")
	case <-ctx.Done():
		server.Close()
		return nil, ctx.Err()
	}

	server.Close()

	// Step 3: Exchange public_token for access_token
	result, err := ps.exchangeToken(ctx, cb.PublicToken)
	if err != nil {
		return nil, fmt.Errorf("exchange token: %w", err)
	}

	result.InstitutionID = cb.InstitutionID

	// Step 4: Get account details
	accounts, err := ps.getAccounts(ctx, result.AccessToken)
	if err != nil {
		slog.Warn("failed to fetch accounts, continuing without details", "error", err)
	} else {
		result.Accounts = accounts
	}

	return result, nil
}

type linkCallback struct {
	PublicToken   string `json:"public_token"`
	InstitutionID string `json:"institution_id"`
}

func (ps *PlaidSetup) createLinkToken(ctx context.Context) (string, error) {
	payload := map[string]any{
		"client_id":    ps.ClientID,
		"secret":       ps.Secret,
		"client_name":  "Budget Daemon",
		"language":     "en",
		"country_codes": []string{"US"},
		"user":         map[string]string{"client_user_id": "budget-user-1"},
		"products":     []string{"transactions"},
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", ps.BaseURL+"/link/token/create", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("plaid error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		LinkToken string `json:"link_token"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}

	return result.LinkToken, nil
}

func (ps *PlaidSetup) exchangeToken(ctx context.Context, publicToken string) (*PlaidLinkResult, error) {
	payload := map[string]string{
		"client_id":    ps.ClientID,
		"secret":       ps.Secret,
		"public_token": publicToken,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", ps.BaseURL+"/item/public_token/exchange", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plaid error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ItemID      string `json:"item_id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &PlaidLinkResult{
		AccessToken: result.AccessToken,
		ItemID:      result.ItemID,
	}, nil
}

func (ps *PlaidSetup) getAccounts(ctx context.Context, accessToken string) ([]PlaidAccount, error) {
	payload := map[string]string{
		"client_id":    ps.ClientID,
		"secret":       ps.Secret,
		"access_token": accessToken,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", ps.BaseURL+"/accounts/get", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plaid error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Accounts []struct {
			AccountID string `json:"account_id"`
			Name      string `json:"name"`
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	var accounts []PlaidAccount
	for _, a := range result.Accounts {
		accounts = append(accounts, PlaidAccount{
			ID:      a.AccountID,
			Name:    a.Name,
			Type:    a.Type,
			Subtype: a.Subtype,
		})
	}

	return accounts, nil
}

func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Budget Daemon (localhost)"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return tls.X509KeyPair(certPEM, keyPEM)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Start()
}

const plaidLinkHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Budget Daemon - Connect Account</title>
    <script src="https://cdn.plaid.com/link/v2/stable/link-initialize.js"></script>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            margin: 0;
            background: #f5f5f5;
        }
        .container {
            text-align: center;
            padding: 2rem;
            background: white;
            border-radius: 12px;
            box-shadow: 0 2px 8px rgba(0,0,0,0.1);
            max-width: 400px;
        }
        h1 { font-size: 1.5rem; margin-bottom: 0.5rem; }
        p { color: #666; margin-bottom: 1.5rem; }
        #status { margin-top: 1rem; color: #888; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Budget Daemon</h1>
        <p>Connect your bank account securely via Plaid.</p>
        <div id="status">Opening Plaid Link...</div>
    </div>
    <script>
        const handler = Plaid.create({
            token: '%s',
            onSuccess: function(public_token, metadata) {
                document.getElementById('status').textContent = 'Connected! Saving...';
                fetch('/callback', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({
                        public_token: public_token,
                        institution_id: metadata.institution ? metadata.institution.institution_id : ''
                    })
                }).then(resp => resp.text()).then(html => {
                    document.body.innerHTML = html;
                });
            },
            onLoad: function() {
                document.getElementById('status').textContent = 'Plaid Link loaded. Follow the prompts...';
            },
            onExit: function(err, metadata) {
                if (err) {
                    document.getElementById('status').textContent = 'Error: ' + (err.display_message || err.error_message || err.error_code || JSON.stringify(err));
                    console.error('Plaid error:', JSON.stringify(err));
                    console.error('Plaid metadata:', JSON.stringify(metadata));
                } else {
                    document.getElementById('status').textContent = 'Setup cancelled. Close this tab and try again.';
                }
            },
            onEvent: function(eventName, metadata) {
                console.log('Plaid event:', eventName, JSON.stringify(metadata));
            }
        });
        handler.open();
    </script>
</body>
</html>`
