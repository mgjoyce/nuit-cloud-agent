package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

type fakeObserver struct {
	collected []string
	fail      bool
}

func (f *fakeObserver) Known(kind string) bool { return kind == "network" }
func (f *fakeObserver) Collect(_ context.Context, kind string) (any, error) {
	f.collected = append(f.collected, kind)
	if f.fail {
		return nil, io.ErrUnexpectedEOF
	}
	return map[string]any{"interfaces": []any{}, "neighbours": []any{}}, nil
}

func TestObservationsAreSentOnlyWhenAskedForKnownAndDue(t *testing.T) {
	var asked string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		_, _ = io.WriteString(w, asked)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1", true)
	obs := &fakeObserver{}
	c.Observer = obs
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	ctx := context.Background()

	// Nothing is asked for yet, so nothing is read or sent.
	asked = `{"pollIntervalSeconds":30,"tasks":[],"observe":[{"kind":"network","intervalSeconds":300},{"kind":"cpu","intervalSeconds":60}]}`
	if _, err := c.CheckIn(ctx); err != nil {
		t.Fatal(err)
	}
	if len(obs.collected) != 0 || bodies[0]["observations"] != nil {
		t.Fatalf("collected %v, sent %v", obs.collected, bodies[0])
	}
	// Now it is asked for: the known kind goes, the unknown one does not.
	if _, err := c.CheckIn(ctx); err != nil {
		t.Fatal(err)
	}
	sent, _ := bodies[1]["observations"].([]any)
	if len(sent) != 1 || sent[0].(map[string]any)["kind"] != "network" || sent[0].(map[string]any)["observedAt"] != "2026-11-03T12:00:00Z" {
		t.Fatalf("sent %v", bodies[1])
	}
	// Not again inside its interval, and again after it.
	now = now.Add(2 * time.Minute)
	_, _ = c.CheckIn(ctx)
	if bodies[2]["observations"] != nil || len(obs.collected) != 1 {
		t.Errorf("sent again too soon: %v, read %v", bodies[2], obs.collected)
	}
	now = now.Add(4 * time.Minute)
	_, _ = c.CheckIn(ctx)
	if bodies[3]["observations"] == nil {
		t.Error("due again after the interval")
	}
}

func TestAnAnswerWithoutObserveChangesNothingAndAnEmptyListAsksForNone(t *testing.T) {
	answers := []string{`{"observe":[{"kind":"network","intervalSeconds":300}]}`, `{"pollIntervalSeconds":30}`, `{"observe":[]}`}
	var n int
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		_, _ = io.WriteString(w, answers[min(n, len(answers)-1)])
		n++
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1", true)
	c.Observer = &fakeObserver{}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { now = now.Add(10 * time.Minute); return now }
	for i := 0; i < 4; i++ {
		if _, err := c.CheckIn(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if bodies[0]["observations"] != nil || bodies[1]["observations"] == nil {
		t.Errorf("asked for after the first answer: %v %v", bodies[0], bodies[1])
	}
	if bodies[2]["observations"] == nil { // an answer that says nothing about it leaves the request standing
		t.Error("an answer with no observe field must leave the request unchanged")
	}
	if bodies[3]["observations"] != nil { // the empty list withdrew it
		t.Error("an empty list asks for none")
	}
}

func TestAFailedReadIsSkippedAndTheCheckInStillHappens(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"observe":[{"kind":"network","intervalSeconds":60}]}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1", true)
	c.Observer = &fakeObserver{fail: true}
	for i := 0; i < 2; i++ {
		if _, err := c.CheckIn(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got["observations"] != nil {
		t.Errorf("sent %v", got)
	}
}
