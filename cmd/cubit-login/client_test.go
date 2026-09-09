package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestArmRelaySendsTheTokenAndTarget(t *testing.T) {
	var gotToken, gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken, gotPath = r.Header.Get("X-API-Token"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newCubitClient(srv.URL+"/", "tok")
	if err := c.armRelay(context.Background(), "***-***1865", "phone"); err != nil {
		t.Fatalf("armRelay: %v", err)
	}
	if gotToken != "tok" {
		t.Errorf("X-API-Token = %q", gotToken)
	}
	if gotPath != "/api/v1/auth/browser" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["masked_target"] != "***-***1865" || gotBody["method"] != "phone" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestArmRelayExplainsAConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	err := newCubitClient(srv.URL, "tok").armRelay(context.Background(), "x", "sms")
	if err == nil || !strings.Contains(err.Error(), "already awaiting") {
		t.Fatalf("error = %v, want a conflict explanation", err)
	}
}

func TestArmRelayExplainsAnUnguardedInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer srv.Close()

	err := newCubitClient(srv.URL, "").armRelay(context.Background(), "x", "sms")
	if err == nil || !strings.Contains(err.Error(), "api token") {
		t.Fatalf("error = %v, want the unguarded explanation", err)
	}
}

func TestWaitForOTPPollsUntilTheCodeArrives(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"code":"123456"}`)
	}))
	defer srv.Close()

	code, err := newCubitClient(srv.URL, "tok").waitForOTP(context.Background(), time.Millisecond)
	if err != nil {
		t.Fatalf("waitForOTP: %v", err)
	}
	if code != "123456" {
		t.Errorf("code = %q", code)
	}
	if calls < 3 {
		t.Errorf("polled %d times, expected to wait for the code", calls)
	}
}

func TestWaitForOTPGivesUpWhenTheContextExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := newCubitClient(srv.URL, "tok").waitForOTP(ctx, time.Millisecond); err == nil {
		t.Fatal("waitForOTP returned no error after the deadline")
	}
}

func TestHandOverSessionPostsTheJar(t *testing.T) {
	var gotBody struct {
		Cookies []sessionCookie `json:"cookies"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	jar := []sessionCookie{{Name: "token", Value: "v", Domain: ".pluxee.co.il", Path: "/"}}
	if err := newCubitClient(srv.URL, "tok").handOverSession(context.Background(), jar); err != nil {
		t.Fatalf("handOverSession: %v", err)
	}
	if len(gotBody.Cookies) != 1 || gotBody.Cookies[0].Name != "token" {
		t.Errorf("cookies = %v", gotBody.Cookies)
	}
}

func TestHandOverSessionSurfacesARejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"the supplied session is not accepted by pluxee"}`)
	}))
	defer srv.Close()

	err := newCubitClient(srv.URL, "tok").handOverSession(context.Background(),
		[]sessionCookie{{Name: "t", Value: "v"}})
	if err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("error = %v, want the rejection surfaced", err)
	}
}
