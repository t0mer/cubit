// Package notify delivers cubit's run results to the user's own channels.
//
// Three providers are supported, per the project's notification guideline:
// Shoutrrr as the catch-all, GreenAPI for WhatsApp Cloud, and a self-hosted
// go-whatsapp-web-multidevice instance.
//
// Everything here holds credentials, so two rules run through the package:
// they are encrypted at rest with AES-256-GCM, and they are never logged or
// returned by the API in plaintext.
package notify

import (
	"fmt"
	"strings"
)

// Provider identifies how a channel delivers messages.
type Provider string

const (
	// ProviderShoutrrr covers Slack, Discord, Telegram, Gotify, SMTP, ntfy and
	// many more behind a single URL scheme.
	ProviderShoutrrr Provider = "shoutrrr"
	// ProviderGreenAPI is the GreenAPI WhatsApp cloud service.
	ProviderGreenAPI Provider = "greenapi"
	// ProviderWhatsAppWeb is a self-hosted go-whatsapp-web-multidevice.
	ProviderWhatsAppWeb Provider = "whatsapp_web"
)

// ShoutrrrConfig is a single URL, e.g. gotify://host/token or
// telegram://token@telegram?chats=@channel.
type ShoutrrrConfig struct {
	URL string `json:"url,omitempty"`
}

// GreenAPIConfig addresses a GreenAPI instance.
//
// APIURL may be empty for the default host. GreenAPI assigns instances to
// clusters, so a console showing e.g. https://7103.api.greenapi.com must be
// set here.
type GreenAPIConfig struct {
	InstanceID string `json:"instance_id,omitempty"`
	Token      string `json:"token,omitempty"`
	// Phone is international format, digits only, no + or spaces.
	Phone  string `json:"phone,omitempty"`
	APIURL string `json:"api_url,omitempty"`
}

// WhatsAppWebConfig addresses a self-hosted go-whatsapp-web-multidevice.
type WhatsAppWebConfig struct {
	BaseURL  string `json:"base_url,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Channel is one delivery destination.
type Channel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider Provider `json:"provider"`

	Shoutrrr    ShoutrrrConfig    `json:"shoutrrr,omitempty"`
	GreenAPI    GreenAPIConfig    `json:"greenapi,omitempty"`
	WhatsAppWeb WhatsAppWebConfig `json:"whatsapp_web,omitempty"`

	Enabled         bool `json:"enabled"`
	NotifyOnSuccess bool `json:"notify_on_success"`
	NotifyOnFailure bool `json:"notify_on_failure"`
}

// Normalise trims every field. Stray whitespace in a GreenAPI token or
// instance id corrupts the request URL and produces an opaque 400, which is the
// single most common way to misconfigure that provider.
func (c *Channel) Normalise() {
	c.Name = strings.TrimSpace(c.Name)
	c.Provider = Provider(strings.TrimSpace(string(c.Provider)))
	c.Shoutrrr.URL = strings.TrimSpace(c.Shoutrrr.URL)
	c.GreenAPI.InstanceID = strings.TrimSpace(c.GreenAPI.InstanceID)
	c.GreenAPI.Token = strings.TrimSpace(c.GreenAPI.Token)
	c.GreenAPI.Phone = strings.TrimSpace(c.GreenAPI.Phone)
	c.GreenAPI.APIURL = strings.TrimSpace(c.GreenAPI.APIURL)
	c.WhatsAppWeb.BaseURL = strings.TrimSpace(c.WhatsAppWeb.BaseURL)
	c.WhatsAppWeb.Phone = strings.TrimSpace(c.WhatsAppWeb.Phone)
	c.WhatsAppWeb.Username = strings.TrimSpace(c.WhatsAppWeb.Username)
}

// Validate reports whether the channel could actually deliver anything.
func (c *Channel) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("notify: name is required")
	}
	switch c.Provider {
	case ProviderShoutrrr:
		if c.Shoutrrr.URL == "" {
			return fmt.Errorf("notify: shoutrrr url is required")
		}
	case ProviderGreenAPI:
		if c.GreenAPI.InstanceID == "" {
			return fmt.Errorf("notify: greenapi instance_id is required")
		}
		if c.GreenAPI.Token == "" {
			return fmt.Errorf("notify: greenapi token is required")
		}
		if c.GreenAPI.Phone == "" {
			return fmt.Errorf("notify: greenapi phone is required")
		}
	case ProviderWhatsAppWeb:
		if c.WhatsAppWeb.BaseURL == "" {
			return fmt.Errorf("notify: whatsapp_web base_url is required")
		}
		if c.WhatsAppWeb.Phone == "" {
			return fmt.Errorf("notify: whatsapp_web phone is required")
		}
	default:
		return fmt.Errorf("notify: unknown provider %q", c.Provider)
	}
	return nil
}

// Redacted returns a copy safe to hand back over the API: every secret is
// replaced by a fixed marker, so a caller can see that something is set
// without learning what.
func (c Channel) Redacted() Channel {
	out := c
	out.Shoutrrr.URL = mask(c.Shoutrrr.URL)
	out.GreenAPI.Token = mask(c.GreenAPI.Token)
	out.WhatsAppWeb.Password = mask(c.WhatsAppWeb.Password)
	return out
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return "********"
}
