package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// ResendClient sends emails via the Resend API.
type ResendClient struct {
	apiKey    string
	fromEmail string
	toEmail   string
	client    *http.Client
}

// NewResendClient creates a new Resend email client.
func NewResendClient(apiKey, fromEmail, toEmail string) *ResendClient {
	if apiKey == "" {
		slog.Warn("resend: API key not configured, emails will not be sent")
	}
	return &ResendClient{
		apiKey:    apiKey,
		fromEmail: fromEmail,
		toEmail:   toEmail,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// SendNotification sends a generic notification email.
// If 'to' is a valid email address, it is used as the recipient.
// Falls back to the configured toEmail only when 'to' is empty.
// Returns the Resend message ID for tracking.
func (c *ResendClient) SendNotification(to, subject, body string) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("resend: API key not configured")
	}
	recipient := to
	if recipient == "" {
		recipient = c.toEmail
	}
	payload := map[string]interface{}{
		"from":    c.fromEmail,
		"to":      []string{recipient},
		"subject": subject,
		"html":    fmt.Sprintf("<div>%s</div><hr><p style=\"color:#888;font-size:12px\">Sent by Vela AI — %s</p>", body, to),
	}
	return c.doSend(payload)
}

// SendContactNotification sends a contact form submission notification via Resend.
func (c *ResendClient) SendContactNotification(name, email, message string) error {
	if c.apiKey == "" {
		return fmt.Errorf("resend: API key not configured")
	}
	payload := map[string]interface{}{
		"from":    c.fromEmail,
		"to":      []string{c.toEmail},
		"subject": fmt.Sprintf("Vela Contact: %s (%s)", name, email),
		"html": fmt.Sprintf(`<h2>New Contact Form Submission</h2>
<p><strong>Name:</strong> %s</p>
<p><strong>Email:</strong> %s</p>
<p><strong>Message:</strong></p>
<blockquote>%s</blockquote>
<hr><p style="color:#888;font-size:12px">Sent from Vela AI Landing Page</p>`, name, email, message),
	}
	_, err := c.doSend(payload)
	return err
}

// SendToCustomer sends an email to any recipient and returns the Resend message ID.
func (c *ResendClient) SendToCustomer(to, subject, htmlBody string) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("resend: API key not configured")
	}
	payload := map[string]interface{}{
		"from":        c.fromEmail,
		"to":          []string{to},
		"subject":     subject,
		"html":        htmlBody,
		"track_opens": true,
	}
	return c.doSend(payload)
}

// doSend makes the HTTP request to the Resend API and returns the message ID.
func (c *ResendClient) doSend(payload map[string]interface{}) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("resend: marshal payload: %w", err)
	}
	req, err := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("resend: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resend: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("resend: HTTP %d", resp.StatusCode)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("resend: decode response: %w", err)
	}
	slog.Info("resend: email sent", "message_id", result.ID)
	return result.ID, nil
}
