package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefusesAPlainHTTPAddressUnlessToldTo(t *testing.T) {
	if _, err := New("http://platform.example", "1", false); err == nil {
		t.Error("plain http must be refused by default")
	}
	if _, err := New("http://platform.example", "1", true); err != nil {
		t.Errorf("explicitly allowed: %v", err)
	}
	if _, err := New("https://platform.example/", "1", false); err != nil {
		t.Errorf("https: %v", err)
	}
	for _, bad := range []string{"", "not a url", "ftp://x", "https://"} {
		if _, err := New(bad, "1", true); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestEnrollSendsTheCodeAndNoCredentialAndReadsTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/enroll" || r.Method != "POST" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("enrolling must not send a credential")
		}
		if r.Header.Get("X-Agent-Version") != "1.2.3" {
			t.Errorf("version header %q", r.Header.Get("X-Agent-Version"))
		}
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["code"] != "nce_abc" || body["hostname"] != "box" {
			t.Errorf("body %v", body)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"machineId":"m1","name":"box","credential":"nca_x","pollIntervalSeconds":30,"taskSigning":{"keys":[{"keyId":"k","publicKey":"p"}]}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1.2.3", true)
	got, err := c.Enroll(context.Background(), "nce_abc", "box")
	if err != nil {
		t.Fatal(err)
	}
	if got.MachineID != "m1" || got.Credential != "nca_x" || len(got.TaskSigning.Keys) != 1 || got.TaskSigning.Keys[0].KeyID != "k" {
		t.Errorf("%+v", got)
	}
}

func TestRoutineCallsSendTheCredentialAndANon2xxIsAStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nca_secret" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"That is not a code or credential this platform recognises"}`))
			return
		}
		_, _ = w.Write([]byte(`{"machineId":"m1","name":"box","pollIntervalSeconds":45,"tasks":[{"keyId":"k","payload":"p","signature":"s"}]}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1", true)
	c.Credential = "nca_wrong"
	_, err := c.CheckIn(context.Background())
	se, ok := err.(*StatusError)
	if !ok || !se.Unauthorized() || se.Message == "" {
		t.Fatalf("got %v", err)
	}
	c.Credential = "nca_secret"
	got, err := c.CheckIn(context.Background())
	if err != nil || got.PollIntervalSeconds != 45 || len(got.Tasks) != 1 || got.Tasks[0].Payload != "p" {
		t.Errorf("%+v %v", got, err)
	}
}

func TestAConflictOnReportingIsNotAnError(t *testing.T) {
	status := 409
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/tasks/t%2F1/result" && r.URL.EscapedPath() != "/api/agent/tasks/t%2F1/result" {
			t.Errorf("path %q", r.URL.EscapedPath())
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"That task has already finished"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1", true)
	c.Credential = "nca_x"
	if err := c.ReportTask(context.Background(), "t/1", Report{Status: "done"}); err != nil {
		t.Errorf("409 means there is nothing more to do: %v", err)
	}
	status = 404
	if err := c.ReportTask(context.Background(), "t/1", Report{Status: "done"}); err == nil {
		t.Error("a 404 is an error")
	}
}

func TestRotateReturnsTheNewCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"credential":"nca_fresh"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1", true)
	c.Credential = "nca_old"
	got, err := c.RotateCredential(context.Background())
	if err != nil || got != "nca_fresh" {
		t.Errorf("%q %v", got, err)
	}
}
