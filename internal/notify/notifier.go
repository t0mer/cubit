package notify

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Outcome selects which channels a message goes to.
type Outcome int

const (
	// Success is a completed run: the balance was read.
	Success Outcome = iota
	// Failure is a run that did not complete, including a partial failure.
	Failure
)

// DefaultTimeout bounds a single delivery attempt.
const DefaultTimeout = 15 * time.Second

// Notifier fans a message out to every channel that asked for it.
//
// Delivery is best effort by design: the guideline requires that sending never
// blocks or fails the primary operation, so every error here is logged and
// swallowed. A dead WhatsApp endpoint must not cost the user their balance.
type Notifier struct {
	store  *Store
	client *http.Client
	log    *slog.Logger
}

// NewNotifier returns a Notifier. A nil client or logger gets a sane default.
func NewNotifier(store *Store, client *http.Client, logger *slog.Logger) *Notifier {
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{store: store, client: client, log: logger}
}

// Notify delivers message to every enabled channel matching outcome.
//
// It returns once every attempt has finished or the context has expired,
// whichever comes first, and never returns an error: there is nothing a caller
// could usefully do about a failed notification that is not already logged.
func (n *Notifier) Notify(ctx context.Context, outcome Outcome, message string) {
	if n == nil || n.store == nil {
		return
	}
	channels, err := n.store.List()
	if err != nil {
		n.log.Error("could not read the notification channels", "error", err)
		return
	}

	var wg sync.WaitGroup
	for _, ch := range channels {
		if !ch.Enabled || !wants(ch, outcome) {
			continue
		}
		wg.Add(1)
		go func(ch Channel) {
			defer wg.Done()
			if err := Send(ctx, n.client, ch, message); err != nil {
				// Named, not detailed: the channel's config holds secrets.
				n.log.Warn("could not deliver a notification",
					"channel", ch.Name, "provider", ch.Provider, "error", err)
				return
			}
			n.log.Info("notification sent", "channel", ch.Name, "provider", ch.Provider)
		}(ch)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		// The attempts carry the same context and will unwind on their own.
		n.log.Warn("gave up waiting for notifications to send", "error", ctx.Err())
	}
}

func wants(ch Channel, outcome Outcome) bool {
	if outcome == Success {
		return ch.NotifyOnSuccess
	}
	return ch.NotifyOnFailure
}
