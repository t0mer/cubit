package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GreenAPI's request shape, exactly as the guideline documents it:
// POST {apiUrl}/waInstance{instanceId}/sendMessage/{token}
// with {"chatId": "{phone}@c.us", "message": "..."}
func TestGreenAPIRequestShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"idMessage":"abc"}`)
	}))
	defer srv.Close()

	ch := Channel{Provider: ProviderGreenAPI, Name: "wa", GreenAPI: GreenAPIConfig{
		InstanceID: "7103", Token: "tok123", Phone: "972501234567", APIURL: srv.URL,
	}}
	if err := Send(context.Background(), srv.Client(), ch, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if want := "/waInstance7103/sendMessage/tok123"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody["chatId"] != "972501234567@c.us" {
		t.Errorf("chatId = %q", gotBody["chatId"])
	}
	if gotBody["message"] != "hello" {
		t.Errorf("message = %q", gotBody["message"])
	}
}

// The guideline calls this out: a phone that already carries @c.us was being
// double-suffixed, producing a 400 nobody could diagnose.
func TestGreenAPIDoesNotDoubleSuffixTheChatID(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	ch := Channel{Provider: ProviderGreenAPI, Name: "wa", GreenAPI: GreenAPIConfig{
		InstanceID: "1", Token: "t", Phone: "972501234567@c.us", APIURL: srv.URL,
	}}
	if err := Send(context.Background(), srv.Client(), ch, "hi"); err != nil {
		t.Fatal(err)
	}
	if gotBody["chatId"] != "972501234567@c.us" {
		t.Errorf("chatId = %q, want no second @c.us", gotBody["chatId"])
	}
}

func TestGreenAPISurfacesAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"bad instance"}`)
	}))
	defer srv.Close()

	ch := Channel{Provider: ProviderGreenAPI, Name: "wa", GreenAPI: GreenAPIConfig{
		InstanceID: "1", Token: "t", Phone: "9725", APIURL: srv.URL,
	}}
	if err := Send(context.Background(), srv.Client(), ch, "hi"); err == nil {
		t.Fatal("Send reported success for a 400")
	}
}

// An error must never carry the token: it ends up in logs.
func TestGreenAPIErrorDoesNotLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	const token = "super-secret-token"
	ch := Channel{Provider: ProviderGreenAPI, Name: "wa", GreenAPI: GreenAPIConfig{
		InstanceID: "1", Token: token, Phone: "9725", APIURL: srv.URL,
	}}
	err := Send(context.Background(), srv.Client(), ch, "hi")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaked the token: %v", err)
	}
}

func TestWhatsAppWebPostsWithBasicAuth(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"code":"SUCCESS"}`)
	}))
	defer srv.Close()

	ch := Channel{Provider: ProviderWhatsAppWeb, Name: "self", WhatsAppWeb: WhatsAppWebConfig{
		BaseURL: srv.URL, Phone: "972501234567", Username: "u", Password: "p",
	}}
	if err := Send(context.Background(), srv.Client(), ch, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/send/message" {
		t.Errorf("path = %q", gotPath)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	if gotAuth != want {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["phone"] != "972501234567@s.whatsapp.net" {
		t.Errorf("phone = %v", gotBody["phone"])
	}
	if gotBody["message"] != "hello" {
		t.Errorf("message = %v", gotBody["message"])
	}
}

func TestWhatsAppWebWithoutCredentialsSendsNoAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	ch := Channel{Provider: ProviderWhatsAppWeb, Name: "self", WhatsAppWeb: WhatsAppWebConfig{
		BaseURL: srv.URL, Phone: "9725",
	}}
	if err := Send(context.Background(), srv.Client(), ch, "hi"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none", gotAuth)
	}
}

func TestUnknownProviderIsAnError(t *testing.T) {
	ch := Channel{Provider: "pigeon", Name: "x"}
	if err := Send(context.Background(), http.DefaultClient, ch, "hi"); err == nil {
		t.Fatal("Send accepted an unknown provider")
	}
}
