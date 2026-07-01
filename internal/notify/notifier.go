package notify

import "context"

// Alert represents a notification to be sent.
type Alert struct {
	Subject string // Short summary (used as SMS body or email subject)
	Body    string // Longer detail (used in email body)
	Channel string // "sms", "email", "both"
}

// Notifier sends alerts via a specific channel.
type Notifier interface {
	Send(ctx context.Context, alert Alert) error
	Name() string
}

// Dispatcher routes alerts to the appropriate notifier(s).
type Dispatcher struct {
	sms   Notifier
	email Notifier
}

func NewDispatcher(sms, email Notifier) *Dispatcher {
	return &Dispatcher{sms: sms, email: email}
}

func (d *Dispatcher) Dispatch(ctx context.Context, alert Alert) error {
	switch alert.Channel {
	case "sms":
		if d.sms != nil {
			return d.sms.Send(ctx, alert)
		}
	case "email":
		if d.email != nil {
			return d.email.Send(ctx, alert)
		}
	case "both":
		var firstErr error
		if d.sms != nil {
			if err := d.sms.Send(ctx, alert); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if d.email != nil {
			if err := d.email.Send(ctx, alert); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	return nil
}
