package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cubit/internal/notify"
)

// Notification channels are configured entirely over the API: cubit has no web
// UI, so the Settings form in the project guideline becomes these endpoints.
//
// Every response is redacted. A caller can see that a token is set but never
// what it is, so a leaked listing does not hand over the user's WhatsApp.

// maxChannelBytes caps a channel body. Larger than an OTP, far smaller than a
// cookie jar.
const maxChannelBytes = 16 << 10

func (h *Handler) decodeChannel(w http.ResponseWriter, r *http.Request) (notify.Channel, bool) {
	var ch notify.Channel
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChannelBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ch); err != nil {
		// Never echoed: the body holds provider credentials.
		h.log.Debug("rejected a notification channel body", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "body must be a json notification channel",
		})
		return notify.Channel{}, false
	}
	return ch, true
}

// handleListChannels returns every configured channel, redacted.
func (h *Handler) handleListChannels(w http.ResponseWriter, _ *http.Request) {
	channels, err := h.channels.List()
	if err != nil {
		h.log.Error("could not read the notification channels", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not read the notification channels",
		})
		return
	}
	out := make([]notify.Channel, 0, len(channels))
	for _, c := range channels {
		out = append(out, c.Redacted())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAddChannel stores a new channel.
func (h *Handler) handleAddChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.decodeChannel(w, r)
	if !ok {
		return
	}
	created, err := h.channels.Add(ch)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.log.Info("notification channel added", "name", created.Name, "provider", created.Provider)
	writeJSON(w, http.StatusCreated, created.Redacted())
}

// handleUpdateChannel replaces a channel.
func (h *Handler) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.decodeChannel(w, r)
	if !ok {
		return
	}
	ch.ID = chi.URLParam(r, "id")

	switch err := h.channels.Update(ch); {
	case err == nil:
		h.log.Info("notification channel updated", "name", ch.Name, "provider", ch.Provider)
		writeJSON(w, http.StatusOK, ch.Redacted())
	case errors.Is(err, notify.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such channel"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

// handleDeleteChannel removes a channel.
func (h *Handler) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	switch err := h.channels.Delete(chi.URLParam(r, "id")); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, notify.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such channel"})
	default:
		h.log.Error("could not delete a notification channel", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not delete the channel",
		})
	}
}

// handleTestChannel sends a real message using the values in the request,
// without saving them, so a configuration can be validated before it is stored.
func (h *Handler) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.decodeChannel(w, r)
	if !ok {
		return
	}
	ch.Normalise()
	if err := ch.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := notify.Send(r.Context(), nil, ch, testMessage(h.version)); err != nil {
		h.log.Warn("notification test failed", "name", ch.Name, "provider", ch.Provider, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "test message sent",
	})
}

func testMessage(version string) string {
	return "Cubit test message. If you are reading this, the channel works. (cubit " + version + ")"
}
