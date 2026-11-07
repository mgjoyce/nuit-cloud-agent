package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mgjoyce/nuit-cloud-agent/internal/client"
	"github.com/mgjoyce/nuit-cloud-agent/internal/controlplane"
	"github.com/mgjoyce/nuit-cloud-agent/internal/profiles"
	"github.com/mgjoyce/nuit-cloud-agent/internal/taskverify"
)

const (
	cpSecretSched = "FIXTURE-SCHEDULER-TOKEN-7f3a91c2"
	cpSecretKey   = "FIXTURE-TLS-PRIVATE-KEY-b81d44e0"
	cpSecretProbe = "FIXTURE-PROBE-UPDATE-5c0e22aaxxxx"
	cpRev         = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	cpDigest      = "@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// fakeSource stands in for the platform's configuration endpoint.
type fakeSource struct {
	err  error
	body map[string]any
}

func (f fakeSource) GetJSON(_ context.Context, _ string, _ int64, out any) error {
	if f.err != nil {
		return f.err
	}
	raw, _ := json.Marshal(f.body)
	return json.Unmarshal(raw, out)
}

func cpConfig() map[string]any {
	return map[string]any{
		"revision":  cpRev,
		"scheduler": map[string]any{"env": map[string]any{"RENEWAL_SWEEP_TOKEN": cpSecretSched}},
		"tlsFront":  map[string]any{"certPem": "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n", "keyPem": "-----BEGIN PRIVATE KEY-----\n" + cpSecretKey + "\n-----END PRIVATE KEY-----\n"},
		"probe":     map[string]any{"name": "platform-probe", "updateToken": cpSecretProbe, "bootstrapToken": "bootstrap-token-0123456789abcdef"},
		"missing":   []any{},
	}
}

func cpTask(name string, services ...string) *taskverify.Task {
	list := []any{}
	images := map[string]any{}
	for _, s := range services {
		list = append(list, s)
		images[s] = "ghcr.io/example/" + s + cpDigest
	}
	return &taskverify.Task{Name: name, TimeoutSeconds: 60, Params: map[string]any{"profile": "nuit-cloud-control-plane", "controlPlane": map[string]any{"services": list, "images": images}}}
}

func cpRunner(t *testing.T, src fakeSource, f *fakeProfiles) (Runner, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cp")
	r := withProfiles(f)
	r.ControlPlane = controlplane.Provisioner{Source: src, Dir: dir, PlatformURL: "https://agent.example"}
	return r, dir
}

func reportText(t *testing.T, rep client.Report) string {
	t.Helper()
	raw, _ := json.Marshal(rep)
	return rep.Output + "\n" + string(raw)
}

func assertNoSecrets(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range []string{cpSecretSched, cpSecretKey, cpSecretProbe} {
		if strings.Contains(text, s) {
			t.Errorf("%s holds a secret value (%s…)", what, s[:14])
		}
	}
}

func TestTheControlPlaneApplyGivesTheScriptsOnlyPlacesAndNeverASecret(t *testing.T) {
	f := &fakeProfiles{res: profiles.Result{Features: []profiles.Feature{{Name: "ncs_cp_scheduler", Status: "applied"}}}}
	r, dir := cpRunner(t, fakeSource{body: cpConfig()}, f)
	rep := r.Run(context.Background(), cpTask("profile-apply", "scheduler", "tls-front", "probe"))
	if rep.Status != "done" || len(f.calls) != 1 || f.calls[0] != "apply nuit-cloud-control-plane" {
		t.Fatalf("%+v %v", rep, f.calls)
	}
	env := strings.Join(f.env, "\n")
	for _, want := range []string{"NCS_CP_SERVICES=scheduler tls-front probe", "NCS_CP_CONFIG_DIR=" + dir, "NCS_CP_REVISION=" + cpRev, "NCS_CP_IMAGE_TLS_FRONT=ghcr.io/example/tls-front" + cpDigest} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %s in\n%s", want, env)
		}
	}
	assertNoSecrets(t, "the environment handed to the scripts", env)
	if b, _ := os.ReadFile(filepath.Join(dir, "scheduler.env")); !strings.Contains(string(b), cpSecretSched) {
		t.Error("the agent did not write the secret itself")
	}
	assertNoSecrets(t, "the task's report", reportText(t, rep))
}

func TestTheControlPlaneCheckWritesNothing(t *testing.T) {
	f := &fakeProfiles{exit: 1, res: profiles.Result{Features: []profiles.Feature{{Name: "ncs_cp_scheduler", Status: "drift"}}}}
	r, dir := cpRunner(t, fakeSource{body: cpConfig()}, f)
	rep := r.Run(context.Background(), cpTask("profile-check", "scheduler"))
	if rep.Status != "done" || f.calls[0] != "check nuit-cloud-control-plane" {
		t.Fatalf("%+v", rep)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a check wrote to the machine")
	}
}

// a script that prints a secret (it must not, but a result must never carry one if it does) and every failure path
func TestNoSecretReachesTheOutputTheResultOrAnError(t *testing.T) {
	leaky := profiles.Result{
		Output:   "starting with token " + cpSecretSched + "\n" + cpSecretKey,
		Features: []profiles.Feature{{Name: "ncs_cp_probe", Status: "failed", Notes: []string{"the probe env has " + cpSecretProbe}}},
	}
	f := &fakeProfiles{exit: 1, res: leaky}
	r, _ := cpRunner(t, fakeSource{body: cpConfig()}, f)
	for _, name := range []string{"profile-apply", "profile-check"} {
		rep := r.Run(context.Background(), cpTask(name, "scheduler", "tls-front", "probe"))
		text := reportText(t, rep)
		assertNoSecrets(t, name+" with a leaky script", text)
		if !strings.Contains(text, "[redacted]") {
			t.Errorf("%s: the leak was not redacted: %s", name, text)
		}
	}
	// the profile runner itself fails with a message holding a secret
	f = &fakeProfiles{err: errors.New("could not start: token " + cpSecretSched)}
	r, _ = cpRunner(t, fakeSource{body: cpConfig()}, f)
	rep := r.Run(context.Background(), cpTask("profile-apply", "scheduler"))
	if rep.Status != "failed" {
		t.Fatalf("%+v", rep)
	}
	assertNoSecrets(t, "a failed run's report", reportText(t, rep))
	// the platform refuses, is unavailable, answers nonsense, or its answer is unusable: each fails, quoting no secret
	bad := cpConfig()
	bad["scheduler"] = map[string]any{"env": map[string]any{"RENEWAL_SWEEP_TOKEN": cpSecretSched + "\nX=1"}}
	for name, src := range map[string]fakeSource{
		"403":      {err: &client.StatusError{Status: 403, Message: "no"}},
		"503":      {err: &client.StatusError{Status: 503, Message: "later"}},
		"401":      {err: &client.StatusError{Status: 401, Message: "unknown credential"}},
		"unusable": {body: bad},
		"too big":  {err: client.ErrTooLarge},
		"garbled":  {err: client.ErrUnreadable},
		"network":  {err: errors.New("dial tcp 10.0.0.1:443: connection refused")},
	} {
		r, dir := cpRunner(t, src, &fakeProfiles{})
		rep := r.Run(context.Background(), cpTask("profile-apply", "scheduler"))
		if rep.Status != "failed" {
			t.Errorf("%s: %+v", name, rep)
		}
		assertNoSecrets(t, name, reportText(t, rep))
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s: something was written", name)
		}
	}
}

func TestTheControlPlaneRefusalsAreSaidPlainly(t *testing.T) {
	r, _ := cpRunner(t, fakeSource{err: &client.StatusError{Status: http.StatusForbidden, Message: "x"}}, &fakeProfiles{})
	if rep := r.Run(context.Background(), cpTask("profile-apply", "probe")); rep.Status != "failed" || !strings.Contains(rep.Output, "not a control-plane host") {
		t.Errorf("%+v", rep)
	}
	r, _ = cpRunner(t, fakeSource{err: &client.StatusError{Status: http.StatusServiceUnavailable, Message: "x"}}, &fakeProfiles{})
	if rep := r.Run(context.Background(), cpTask("profile-apply", "probe")); rep.Status != "failed" || !strings.Contains(rep.Output, "retryable") {
		t.Errorf("%+v", rep)
	}
}

func TestAHostileOrWrongControlPlaneParameterChangesNothing(t *testing.T) {
	f := &fakeProfiles{}
	r, dir := cpRunner(t, fakeSource{body: cpConfig()}, f)
	img := "ghcr.io/example/p" + cpDigest
	for name, params := range map[string]map[string]any{
		"none":         {"profile": "nuit-cloud-control-plane"},
		"a tag":        {"profile": "nuit-cloud-control-plane", "controlPlane": map[string]any{"services": []any{"probe"}, "images": map[string]any{"probe": "ghcr.io/example/p:latest"}}},
		"extra key":    {"profile": "nuit-cloud-control-plane", "controlPlane": map[string]any{"services": []any{"probe"}, "images": map[string]any{"probe": img}, "shell": "rm -rf /"}},
		"unknown":      {"profile": "nuit-cloud-control-plane", "controlPlane": map[string]any{"services": []any{"sshd"}, "images": map[string]any{"sshd": img}}},
		"injection":    {"profile": "nuit-cloud-control-plane", "controlPlane": map[string]any{"services": []any{"probe"}, "images": map[string]any{"probe": "p@sha256:" + strings.Repeat("a", 64) + "; reboot"}}},
		"other, given": {"profile": "nuit-cloud-server", "controlPlane": map[string]any{"services": []any{"probe"}, "images": map[string]any{"probe": img}}},
	} {
		rep := r.Run(context.Background(), &taskverify.Task{Name: "profile-apply", TimeoutSeconds: 30, Params: params})
		if rep.Status != "failed" {
			t.Errorf("%s was accepted: %+v", name, rep)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("scripts ran: %v", f.calls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("something was written")
	}
	// an agent built without it refuses rather than running the scripts with nothing
	plain := withProfiles(&fakeProfiles{})
	if rep := plain.Run(context.Background(), cpTask("profile-apply", "probe")); rep.Status != "failed" {
		t.Errorf("%+v", rep)
	}
}
