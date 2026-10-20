package taskverify

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

type vectors struct {
	MachineID   string              `json:"machineId"`
	TrustedKeys []Key               `json:"trustedKeys"`
	Tasks       map[string]Envelope `json:"tasks"`
}

func load(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var (
	inWindow = time.Date(2026, 10, 20, 12, 5, 0, 0, time.UTC)
	catalog  = []string{"ping", "survey", "profile-check", "profile-apply"}
)

func ctx(v vectors) Context {
	return Context{MachineID: v.MachineID, Now: inWindow, KnownTasks: catalog}
}

// What the TypeScript platform signs, this agent verifies: the whole point of the test vectors.
func TestAcceptsWhatThePlatformSigns(t *testing.T) {
	v := load(t)
	for name, env := range v.Tasks {
		task, err := Verify(env, v.TrustedKeys, ctx(v))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if task.MachineID != v.MachineID || task.V != 1 || task.TimeoutSeconds == 0 {
			t.Errorf("%s: unexpected task %+v", name, task)
		}
	}
	task, _ := Verify(v.Tasks["profileApply"], v.TrustedKeys, ctx(v))
	if task.Name != "profile-apply" || task.Params["profile"] != "nuit-cloud-server" {
		t.Errorf("parameters not read from the signed bytes: %+v", task)
	}
}

// Characters Go's encoder would escape (<, >, &) and non-ASCII must not matter: the signed bytes are verified as sent.
func TestVerifiesTheBytesAsSentNotARebuiltDocument(t *testing.T) {
	v := load(t)
	task, err := Verify(v.Tasks["tricky"], v.TrustedKeys, ctx(v))
	if err != nil {
		t.Fatal(err)
	}
	if task.Params["note"] != "<b>&amp; café ✓</b>" {
		t.Errorf("got %q", task.Params["note"])
	}
}

func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted, want refusal %q", want)
	}
	if !IsRefusal(err) || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestRefusesAForgedSignature(t *testing.T) {
	v := load(t)
	env := v.Tasks["ping"]
	sig := []byte(env.Signature)
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	env.Signature = string(sig)
	_, err := Verify(env, v.TrustedKeys, ctx(v))
	refusedWith(t, err, "the signature does not verify")
}

func TestRefusesAnAlteredPayload(t *testing.T) {
	v := load(t)
	env := v.Tasks["profileApply"]
	payload, _ := base64.RawURLEncoding.DecodeString(env.Payload)
	var doc map[string]any
	_ = json.Unmarshal(payload, &doc)
	doc["params"] = map[string]any{"profile": "nuit-container-service"}
	altered, _ := json.Marshal(doc)
	env.Payload = base64.RawURLEncoding.EncodeToString(altered)
	_, err := Verify(env, v.TrustedKeys, ctx(v))
	refusedWith(t, err, "the signature does not verify")
}

func TestRefusesAKeyItDoesNotTrust(t *testing.T) {
	v := load(t)
	_, err := Verify(v.Tasks["ping"], nil, ctx(v))
	refusedWith(t, err, "signed by a key this machine does not trust")

	// signed properly by an attacker's own key, presented under the trusted key id
	pub, priv, _ := ed25519.GenerateKey(nil)
	_ = pub
	env := v.Tasks["ping"]
	payload, _ := base64.RawURLEncoding.DecodeString(env.Payload)
	env.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, append([]byte(SigningDomain), payload...)))
	_, err = Verify(env, v.TrustedKeys, ctx(v))
	refusedWith(t, err, "the signature does not verify")
}

func TestRefusesATaskForAnotherMachine(t *testing.T) {
	v := load(t)
	c := ctx(v)
	c.MachineID = "99999999-9999-9999-9999-999999999999"
	_, err := Verify(v.Tasks["ping"], v.TrustedKeys, c)
	refusedWith(t, err, "the task is for another machine")
}

func TestRefusesAnExpiredTask(t *testing.T) {
	v := load(t)
	c := ctx(v)
	c.Now = time.Date(2026, 10, 20, 12, 10, 0, 0, time.UTC) // exactly at expiry: expired
	_, err := Verify(v.Tasks["ping"], v.TrustedKeys, c)
	refusedWith(t, err, "the task has expired")
}

// A task the platform really signed but this agent must not run can be reported on; anything unauthentic cannot.
func TestARefusalNamesTheTaskOnlyWhenItIsAuthentic(t *testing.T) {
	v := load(t)
	c := ctx(v)
	c.KnownTasks = []string{"ping"}
	_, err := Verify(v.Tasks["profileApply"], v.TrustedKeys, c)
	var r *Refusal
	if !errors.As(err, &r) || r.Task == nil || r.Task.ID != "aaaaaaaa-0000-0000-0000-000000000003" {
		t.Fatalf("authentic refusal should carry the task: %+v", err)
	}
	env := v.Tasks["ping"]
	env.Signature = v.Tasks["survey"].Signature
	_, err = Verify(env, v.TrustedKeys, ctx(v))
	if !errors.As(err, &r) || r.Task != nil {
		t.Fatalf("an unauthentic task must not be named: %+v", err)
	}
	c = ctx(v)
	c.MachineID = "99999999-9999-9999-9999-999999999999"
	_, err = Verify(v.Tasks["ping"], v.TrustedKeys, c)
	if !errors.As(err, &r) || r.Task != nil {
		t.Fatalf("another machine's task is not ours to report on: %+v", err)
	}
}

func TestRefusesAReplay(t *testing.T) {
	v := load(t)
	c := ctx(v)
	c.AlreadySeen = func(id string) bool { return id == "aaaaaaaa-0000-0000-0000-000000000001" }
	_, err := Verify(v.Tasks["ping"], v.TrustedKeys, c)
	refusedWith(t, err, "the task was already run")
}

func TestRefusesATaskOutsideItsOwnCatalog(t *testing.T) {
	v := load(t)
	c := ctx(v)
	c.KnownTasks = []string{"ping"}
	_, err := Verify(v.Tasks["profileApply"], v.TrustedKeys, c)
	refusedWith(t, err, "the task is not in this agent's catalog")
}

func TestRefusesGarbageWithoutPanicking(t *testing.T) {
	v := load(t)
	for _, env := range []Envelope{
		{},
		{KeyID: v.TrustedKeys[0].KeyID},
		{KeyID: v.TrustedKeys[0].KeyID, Payload: "!!!", Signature: "!!!"},
		{KeyID: v.TrustedKeys[0].KeyID, Payload: "e30", Signature: "AAAA"},
	} {
		if _, err := Verify(env, v.TrustedKeys, ctx(v)); err == nil || !IsRefusal(err) {
			t.Errorf("%+v: got %v", env, err)
		}
	}
	bad := []Key{{KeyID: v.TrustedKeys[0].KeyID, PublicKey: "short"}}
	_, err := Verify(v.Tasks["ping"], bad, ctx(v))
	refusedWith(t, err, "the trusted key is malformed")
}
