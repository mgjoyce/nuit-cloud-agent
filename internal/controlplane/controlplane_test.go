package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mgjoyce/nuit-cloud-agent/internal/client"
)

const (
	secSched  = "FIXTURE-SCHEDULER-TOKEN-7f3a91c2"
	secKey    = "FIXTURE-TLS-PRIVATE-KEY-b81d44e0"
	secProbeU = "FIXTURE-PROBE-UPDATE-5c0e22aa"
	secProbeB = "FIXTURE-PROBE-BOOTSTRAP-9d17f3b6"
	rev1      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rev2      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	digest    = "@sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

var secrets = []string{secSched, secKey, secProbeU, secProbeB}

func goodConfig(revision string) map[string]any {
	return map[string]any{
		"revision":  revision,
		"scheduler": map[string]any{"env": map[string]any{"RENEWAL_SWEEP_TOKEN": secSched, "JOB_REPORT_URL": "http://10.10.10.140:8796/internal/job-run"}},
		"tlsFront": map[string]any{
			"certPem":  "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
			"keyPem":   "-----BEGIN PRIVATE KEY-----\n" + secKey + "\n-----END PRIVATE KEY-----\n",
			"upstream": "10.10.10.140:4173",
		},
		"probe":   map[string]any{"name": "platform-probe", "updateToken": secProbeU + "xxxx", "bootstrapToken": secProbeB + "xxxx"},
		"missing": []any{},
	}
}

// platform is a stand-in for GET /api/agent/control-plane-config.
type platform struct {
	status int
	body   any
	raw    string
	hits   atomic.Int32
	auth   atomic.Value
}

func (p *platform) serve(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		p.auth.Store(r.Header.Get("Authorization"))
		if r.Method != http.MethodGet || r.URL.Path != ConfigPath {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(p.status)
		if p.raw != "" {
			_, _ = w.Write([]byte(p.raw))
			return
		}
		_ = json.NewEncoder(w).Encode(p.body)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "9.9.9", true)
	if err != nil {
		t.Fatal(err)
	}
	c.Credential = "nca_credential"
	return c
}

func params(t *testing.T, services ...string) Params {
	t.Helper()
	list := []any{}
	images := map[string]any{}
	for _, s := range services {
		list = append(list, s)
		images[s] = "ghcr.io/example/" + s + digest
	}
	p, err := ParseParams(map[string]any{"services": list, "images": images})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noSecret(t *testing.T, what string, texts ...string) {
	t.Helper()
	for _, text := range texts {
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s holds a secret value (%s…)", what, s[:14])
			}
		}
	}
}

func TestParamsAreCheckedAsThePlatformChecksThem(t *testing.T) {
	img := func(n string) string { return "ghcr.io/example/" + n + digest }
	good := map[string]any{"services": []any{"probe", "scheduler"}, "images": map[string]any{"scheduler": img("s"), "probe": img("p")}}
	p, err := ParseParams(good)
	if err != nil || strings.Join(p.Services, ",") != "scheduler,probe" || p.Images["probe"] != img("p") {
		t.Fatalf("%+v %v", p, err)
	}
	bad := map[string]map[string]any{
		"extra key":          {"services": []any{"probe"}, "images": map[string]any{"probe": img("p")}, "env": map[string]any{}},
		"no services":        {"images": map[string]any{}},
		"empty services":     {"services": []any{}, "images": map[string]any{}},
		"unknown service":    {"services": []any{"shell"}, "images": map[string]any{"shell": img("x")}},
		"duplicate":          {"services": []any{"probe", "probe"}, "images": map[string]any{"probe": img("p")}},
		"non-string service": {"services": []any{3}, "images": map[string]any{}},
		"no images":          {"services": []any{"probe"}},
		"missing image":      {"services": []any{"probe", "scheduler"}, "images": map[string]any{"probe": img("p")}},
		"a tag":              {"services": []any{"probe"}, "images": map[string]any{"probe": "ghcr.io/example/p:latest"}},
		"a tag and digest":   {"services": []any{"probe"}, "images": map[string]any{"probe": "ghcr.io/example/p:1" + digest + "x"}},
		"short digest":       {"services": []any{"probe"}, "images": map[string]any{"probe": "ghcr.io/example/p@sha256:abc"}},
		"upper case":         {"services": []any{"probe"}, "images": map[string]any{"probe": "GHCR.io/p" + digest}},
		"shell metachar":     {"services": []any{"probe"}, "images": map[string]any{"probe": "p;rm" + digest}},
		"space":              {"services": []any{"probe"}, "images": map[string]any{"probe": "p x" + digest}},
		"image for unlisted": {"services": []any{"probe"}, "images": map[string]any{"probe": img("p"), "scheduler": img("s")}},
		"image not string":   {"services": []any{"probe"}, "images": map[string]any{"probe": 4}},
		"nil":                nil,
	}
	for name, raw := range bad {
		if _, err := ParseParams(raw); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestAnApplyReadsTheConfigurationWithTheAgentsCredentialAndWritesRootOnlyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	pf := &platform{status: 200, body: goodConfig(rev1)}
	prov := Provisioner{Source: pf.serve(t), Dir: dir, PlatformURL: "https://agent.example"}
	env, scrub, err := prov.Prepare(context.Background(), params(t, "scheduler", "tls-front", "probe"), true)
	if err != nil {
		t.Fatal(err)
	}
	if pf.auth.Load() != "Bearer nca_credential" {
		t.Errorf("the agent's own credential was not used: %v", pf.auth.Load())
	}
	modes := map[string]os.FileMode{"scheduler.env": 0o600, "probe.env": 0o600, "tls/key.pem": 0o600, "tls/cert.pem": 0o644, "revision": 0o644}
	for name, want := range modes {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v %v", name, st, err)
		}
	}
	for _, d := range []string{dir, filepath.Join(dir, "tls")} {
		if st, _ := os.Stat(d); st.Mode().Perm() != 0o700 {
			t.Errorf("%s is %v", d, st.Mode())
		}
	}
	read := func(n string) string { b, _ := os.ReadFile(filepath.Join(dir, n)); return string(b) }
	if got := read("scheduler.env"); got != "JOB_REPORT_URL=http://10.10.10.140:8796/internal/job-run\nRENEWAL_SWEEP_TOKEN="+secSched+"\n" {
		t.Errorf("scheduler.env: %q", got)
	}
	if got := read("probe.env"); !strings.Contains(got, "NUIT_CLOUD_API_URL=https://agent.example\n") || !strings.Contains(got, "DYNDNS_PROBE_NAME=platform-probe\n") || !strings.Contains(got, "DYNDNS_UPDATE_TOKEN="+secProbeU) {
		t.Errorf("probe.env: %q", got)
	}
	if read("revision") != rev1+"\n" || !strings.Contains(read("tls/key.pem"), secKey) {
		t.Error("revision or key not written")
	}
	// what the scripts are given: places and non-secret facts only
	joined := strings.Join(env, "\n")
	for _, want := range []string{"NCS_CP_SERVICES=scheduler tls-front probe", "NCS_CP_CONFIG_DIR=" + dir, "NCS_CP_REVISION=" + rev1, "NCS_CP_MISSING=", "NCS_CP_IMAGE_SCHEDULER=ghcr.io/example/scheduler" + digest, "NCS_CP_IMAGE_TLS_FRONT=ghcr.io/example/tls-front" + digest, "NCS_CP_IMAGE_PROBE=ghcr.io/example/probe" + digest, "NCS_CP_TLS_UPSTREAM=10.10.10.140:4173"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the scripts are not given %s:\n%s", want, joined)
		}
	}
	noSecret(t, "the environment for the scripts", joined)
	if scrub("x "+secSched+" y") != "x [redacted] y" || scrub("plain") != "plain" {
		t.Error("the scrubber does not remove a secret value")
	}
}

func TestACheckFetchesButNeverWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	pf := &platform{status: 200, body: goodConfig(rev1)}
	env, _, err := Provisioner{Source: pf.serve(t), Dir: dir}.Prepare(context.Background(), params(t, "scheduler", "probe"), false)
	if err != nil || pf.hits.Load() != 1 {
		t.Fatal(err, pf.hits.Load())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a check wrote to disk")
	}
	if !strings.Contains(strings.Join(env, "\n"), "NCS_CP_REVISION="+rev1) {
		t.Error("the revision the platform has is not passed on for the comparison")
	}
}

func inode(t *testing.T, p string) (os.FileInfo, string) {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	return st, string(b)
}

func TestOnlyFilesWhoseContentChangedAreWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	pf := &platform{status: 200, body: goodConfig(rev1)}
	prov := Provisioner{Source: pf.serve(t), Dir: dir, PlatformURL: "https://agent.example"}
	ps := params(t, "scheduler", "tls-front", "probe")
	if _, _, err := prov.Prepare(context.Background(), ps, true); err != nil {
		t.Fatal(err)
	}
	names := []string{"scheduler.env", "probe.env", "tls/key.pem", "tls/cert.pem", "revision"}
	before := map[string]os.FileInfo{}
	for _, n := range names {
		before[n], _ = inode(t, filepath.Join(dir, n))
	}
	if _, _, err := prov.Prepare(context.Background(), ps, true); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		after, _ := inode(t, filepath.Join(dir, n))
		if !os.SameFile(before[n], after) || !after.ModTime().Equal(before[n].ModTime()) {
			t.Errorf("%s was rewritten although nothing changed", n)
		}
	}
	// a changed token rewrites that file (and the revision) and no other
	cfg := goodConfig(rev2)
	cfg["probe"].(map[string]any)["updateToken"] = secProbeU + "yyyy"
	pf.body = cfg
	if _, _, err := prov.Prepare(context.Background(), ps, true); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		after, _ := inode(t, filepath.Join(dir, n))
		changed := !os.SameFile(before[n], after)
		if want := n == "probe.env" || n == "revision"; changed != want {
			t.Errorf("%s changed=%v want %v", n, changed, want)
		}
	}
	// a file with the right content and a wrong mode has its mode put right without being rewritten
	if err := os.Chmod(filepath.Join(dir, "scheduler.env"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prov.Prepare(context.Background(), ps, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "scheduler.env")); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
}

func TestWritingLeavesNoTemporaryFileAndNeverHalfAFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	prov := Provisioner{Source: (&platform{status: 200, body: goodConfig(rev1)}).serve(t), Dir: dir}
	if _, _, err := prov.Prepare(context.Background(), params(t, "scheduler", "tls-front", "probe"), true); err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(filepath.Base(p), ".new-") {
			leftovers = append(leftovers, p)
		}
		return nil
	})
	if len(leftovers) != 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}
	// a directory that cannot be written to fails without leaving a partial file or leaking the value into the error
	if os.Getuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		_ = os.MkdirAll(ro, 0o700)
		_ = os.Chmod(ro, 0o500)
		defer os.Chmod(ro, 0o700)
		_, scrub, err := Provisioner{Source: (&platform{status: 200, body: goodConfig(rev1)}).serve(t), Dir: filepath.Join(ro, "cp")}.Prepare(context.Background(), params(t, "scheduler"), true)
		if err == nil {
			t.Fatal("an unwritable directory was not an error")
		}
		noSecret(t, "the error", err.Error(), scrub(err.Error()))
	}
}

func TestOnlyTheServicesAskedForAreWrittenAndMissingOnesAreNamed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	cfg := goodConfig(rev1)
	delete(cfg, "probe")
	cfg["missing"] = []any{"control-plane/probe", "control-plane/unheard-of"}
	pf := &platform{status: 200, body: cfg}
	env, _, err := Provisioner{Source: pf.serve(t), Dir: dir}.Prepare(context.Background(), params(t, "scheduler", "probe"), true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "NCS_CP_MISSING=control-plane/probe\n") && !strings.HasSuffix(strings.Split(joined, "NCS_CP_MISSING=")[1], "control-plane/probe") && !strings.Contains(joined, "NCS_CP_MISSING=control-plane/probe") {
		t.Errorf("the missing secret is not named:\n%s", joined)
	}
	if strings.Contains(joined, "unheard-of") {
		t.Error("a name this agent does not know was passed on")
	}
	if _, err := os.Stat(filepath.Join(dir, "probe.env")); !os.IsNotExist(err) {
		t.Error("probe.env was written without a probe secret")
	}
	if _, err := os.Stat(filepath.Join(dir, "tls", "key.pem")); !os.IsNotExist(err) {
		t.Error("the https front's key was written although the task did not ask for that service")
	}
	// a service asked for, with no section and not listed as missing, is missing all the same
	cfg2 := goodConfig(rev1)
	delete(cfg2, "tlsFront")
	pf.body = cfg2
	env, _, err = Provisioner{Source: pf.serve(t), Dir: dir}.Prepare(context.Background(), params(t, "tls-front"), false)
	if err != nil || !strings.Contains(strings.Join(env, "\n"), "NCS_CP_MISSING=control-plane/agent-tls") {
		t.Errorf("%v %v", env, err)
	}
}

func TestEveryAnswerThePlatformCanGiveIsHandledAndNoneQuotesASecret(t *testing.T) {
	big := `{"revision":"` + rev1 + `","pad":"` + strings.Repeat("x", MaxConfigBytes) + `"}`
	cases := []struct {
		name   string
		status int
		body   any
		raw    string
		want   string
	}{
		{"401", 401, map[string]any{"error": "That is not a code or credential this platform recognises"}, "", "401"},
		{"403", 403, map[string]any{"error": "A Nuit Cloud Server is not given the platform's service secrets"}, "", "not a control-plane host"},
		{"503", 503, map[string]any{"error": "try again"}, "", "retryable"},
		{"500", 500, nil, "boom", "500"},
		{"redirect", 302, nil, "", "redirect"},
		{"malformed json", 200, nil, `{"revision": ` + secSched, "could not be read"},
		{"not an object", 200, nil, `["` + secSched + `"]`, "could not be read"},
		{"no revision", 200, map[string]any{"scheduler": map[string]any{"env": map[string]any{"RENEWAL_SWEEP_TOKEN": secSched}}}, "", "no usable revision"},
		{"bad revision", 200, map[string]any{"revision": "not-a-hash"}, "", "no usable revision"},
		{"oversize", 200, nil, big, "larger than"},
		{"bad env value", 200, map[string]any{"revision": rev1, "scheduler": map[string]any{"env": map[string]any{"RENEWAL_SWEEP_TOKEN": secSched + "\nINJECTED=1"}}}, "", "cannot be written to an env file"},
		{"bad env name", 200, map[string]any{"revision": rev1, "scheduler": map[string]any{"env": map[string]any{"bad name": secSched}}}, "", "not allowed"},
		{"padded value", 200, map[string]any{"revision": rev1, "scheduler": map[string]any{"env": map[string]any{"A": " " + secSched}}}, "", "cannot be written"},
		{"bad key", 200, map[string]any{"revision": rev1, "tlsFront": map[string]any{"certPem": "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----", "keyPem": secKey}}, "", "not usable"},
		{"bad probe", 200, map[string]any{"revision": rev1, "probe": map[string]any{"name": "Bad Name", "updateToken": secProbeU + "xxxx", "bootstrapToken": secProbeB + "xxxx"}}, "", "not usable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cp")
			pf := &platform{status: c.status, body: c.body, raw: c.raw}
			if c.status == 302 {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
				}))
				defer srv.Close()
				cl, _ := client.New(srv.URL, "1", true)
				cl.Credential = "nca_x"
				_, scrub, err := Provisioner{Source: cl, Dir: dir}.Prepare(context.Background(), params(t, "scheduler"), true)
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("%v", err)
				}
				_ = scrub
				return
			}
			env, scrub, err := Provisioner{Source: pf.serve(t), Dir: dir}.Prepare(context.Background(), params(t, "scheduler", "tls-front", "probe"), true)
			if c.name == "bad env value" || c.name == "bad env name" || c.name == "padded value" || c.name == "bad key" || c.name == "bad probe" || err != nil {
				if err == nil {
					t.Fatalf("an unusable answer was accepted: %v", env)
				}
				if !strings.Contains(err.Error(), c.want) {
					t.Errorf("error %q does not say %q", err, c.want)
				}
				noSecret(t, "the error", err.Error(), scrub(err.Error()))
				if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
					t.Error("something was written for an answer that was refused")
				}
				return
			}
			t.Fatalf("expected an error for %s", c.name)
		})
	}
}

func TestAnUnreachablePlatformIsAnErrorThatSaysNothingSecret(t *testing.T) {
	cl, _ := client.New("http://127.0.0.1:1", "1", true)
	cl.Credential = "nca_credential"
	_, _, err := Provisioner{Source: cl, Dir: t.TempDir()}.Prepare(context.Background(), params(t, "probe"), true)
	if err == nil || strings.Contains(err.Error(), "nca_credential") {
		t.Errorf("%v", err)
	}
}

func TestAnUpstreamThatIsNotAPrivateAddressIsNotUsed(t *testing.T) {
	for _, u := range []string{"8.8.8.8:4173", "example.com:4173", "10.0.0.1", "10.0.0.1:99999", "10.0.0.1:80;rm", ""} {
		cfg := goodConfig(rev1)
		cfg["tlsFront"].(map[string]any)["upstream"] = u
		env, _, err := Provisioner{Source: (&platform{status: 200, body: cfg}).serve(t), Dir: t.TempDir()}.Prepare(context.Background(), params(t, "tls-front"), false)
		if err != nil || strings.Contains(strings.Join(env, "\n"), "NCS_CP_TLS_UPSTREAM") {
			t.Errorf("%q: %v %v", u, env, err)
		}
	}
}
