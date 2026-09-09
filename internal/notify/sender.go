package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/containrrr/shoutrrr"
)

// DefaultGreenAPIURL is used when a channel names no cluster. GreenAPI assigns
// instances to clusters, so a console showing e.g. https://7103.api.greenapi.com
// must set APIURL explicitly.
const DefaultGreenAPIURL = "https://api.green-api.com"

// Send delivers one message through one channel.
//
// Errors never carry credentials: they are logged, and a token in a log line is
// a token leaked.
func Send(ctx context.Context, client *http.Client, ch Channel, message string) error {
	ch.Normalise()
	if err := ch.Validate(); err != nil {
		return err
	}
	if client == nil {
		client = http.DefaultClient
	}

	switch ch.Provider {
	case ProviderShoutrrr:
		return sendShoutrrr(ch, message)
	case ProviderGreenAPI:
		return sendGreenAPI(ctx, client, ch, message)
	case ProviderWhatsAppWeb:
		return sendWhatsAppWeb(ctx, client, ch, message)
	default:
		return fmt.Errorf("notify: unknown provider %q", ch.Provider)
	}
}

// sendShoutrrr hands the message to shoutrrr, which understands Slack, Discord,
// Telegram, Gotify, SMTP, ntfy and the rest behind one URL scheme.
func sendShoutrrr(ch Channel, message string) error {
	if err := shoutrrr.Send(ch.Shoutrrr.URL, message); err != nil {
		// The URL embeds the token, so report the scheme only.
		return fmt.Errorf("notify: shoutrrr %s: %w", urlScheme(ch.Shoutrrr.URL), err)
	}
	return nil
}

// sendGreenAPI posts to
// {apiUrl}/waInstance{instanceId}/sendMessage/{token}.
func sendGreenAPI(ctx context.Context, client *http.Client, ch Channel, message string) error {
	base := ch.GreenAPI.APIURL
	if base == "" {
		base = DefaultGreenAPIURL
	}
	url := fmt.Sprintf("%s/waInstance%s/sendMessage/%s",
		strings.TrimRight(base, "/"), ch.GreenAPI.InstanceID, ch.GreenAPI.Token)

	body := map[string]string{
		"chatId":  chatID(ch.GreenAPI.Phone),
		"message": message,
	}
	return postJSON(ctx, client, url, body, nil, "greenapi")
}

// sendWhatsAppWeb posts to a self-hosted go-whatsapp-web-multidevice.
func sendWhatsAppWeb(ctx context.Context, client *http.Client, ch Channel, message string) error {
	url := strings.TrimRight(ch.WhatsAppWeb.BaseURL, "/") + "/send/message"
	body := map[string]any{
		"phone":   whatsappJID(ch.WhatsAppWeb.Phone),
		"message": message,
	}
	auth := func(r *http.Request) {
		if ch.WhatsAppWeb.Username != "" || ch.WhatsAppWeb.Password != "" {
			r.SetBasicAuth(ch.WhatsAppWeb.Username, ch.WhatsAppWeb.Password)
		}
	}
	return postJSON(ctx, client, url, body, auth, "whatsapp_web")
}

func postJSON(ctx context.Context, client *http.Client, url string, body any,
	decorate func(*http.Request), provider string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notify: %s: encoding request: %w", provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		// The url carries the token for greenapi, so it is never echoed.
		return fmt.Errorf("notify: %s: building request: %w", provider, redactErr(err, url))
	}
	req.Header.Set("Content-Type", "application/json")
	if decorate != nil {
		decorate(req)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: %s: %w", provider, redactErr(err, url))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify: %s returned http %d: %s",
			provider, resp.StatusCode, firstLine(string(raw)))
	}
	return nil
}

// chatID appends GreenAPI's suffix unless the phone already carries one.
// Double-suffixing was a documented cause of opaque 400s.
func chatID(phone string) string {
	if strings.Contains(phone, "@") {
		return phone
	}
	return phone + "@c.us"
}

// whatsappJID is the equivalent for go-whatsapp-web-multidevice.
func whatsappJID(phone string) string {
	if strings.Contains(phone, "@") {
		return phone
	}
	return phone + "@s.whatsapp.net"
}

func urlScheme(raw string) string {
	if i := strings.Index(raw, ":"); i > 0 {
		return raw[:i]
	}
	return "url"
}

// redactErr removes a url from an error, because a GreenAPI url embeds the token.
func redactErr(err error, url string) error {
	msg := strings.ReplaceAll(err.Error(), url, "<redacted url>")
	return fmt.Errorf("%s", msg)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
