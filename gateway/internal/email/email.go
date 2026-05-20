package email

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/resend/resend-go/v2"
)

// Mailer sends transactional emails. When unconfigured, it logs the
// payload to stdout instead of sending — useful for dev.
type Mailer struct {
	from   string
	client *resend.Client
	log    *slog.Logger
}

func New(apiKey, fromAddr string, log *slog.Logger) *Mailer {
	m := &Mailer{from: fromAddr, log: log}
	if apiKey != "" {
		m.client = resend.NewClient(apiKey)
	}
	return m
}

// Send dispatches the email, or logs it to stdout when no client is
// configured. The error covers transport issues only.
func (m *Mailer) Send(ctx context.Context, to, subject, html, text string) error {
	if m.client == nil {
		m.log.Info("email (logged, not sent — RESEND_API_KEY unset)",
			"from", m.from, "to", to, "subject", subject, "text", text)
		return nil
	}
	_, err := m.client.Emails.SendWithContext(ctx, &resend.SendEmailRequest{
		From:    m.from,
		To:      []string{to},
		Subject: subject,
		Html:    html,
		Text:    text,
	})
	if err != nil {
		return fmt.Errorf("email: send: %w", err)
	}
	return nil
}
