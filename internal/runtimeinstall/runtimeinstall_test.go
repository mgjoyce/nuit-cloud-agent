package runtimeinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func sum(b string) string { h := sha256.Sum256([]byte(b)); return hex.EncodeToString(h[:]) }

func server(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/agent/download/")
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
}

func params(bundle, containerd string) Params {
	return Params{BundleVersion: "v4", BundleSHA256: sum(bundle), ContainerdVersion: "2.2.10", ContainerdSHA256: sum(containerd), CrictlVersion: "1.37.0", CrictlSHA256: sum("crictl bytes"), RootFingerprint: strings.Repeat("a", 64)}
}

func TestStageChecksBothFilesAndHandsOverTheirPaths(t *testing.T) {
	srv := server(t, map[string]string{BundleFile: "bundle bytes", ContainerdFile: "containerd bytes", CrictlFile: "crictl bytes"})
	defer srv.Close()
	env, cleanup, err := Stager{BaseURL: srv.URL, Dir: t.TempDir()}.Stage(context.Background(), params("bundle bytes", "containerd bytes"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"NCS_RUNTIME_BUNDLE=", "NCS_CONTAINERD_TARBALL=", "NCS_RUNTIME_ROOT_FINGERPRINT=" + strings.Repeat("a", 64), "NCS_RUNTIME_BUNDLE_SHA256=" + sum("bundle bytes")} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lacks %q:\n%s", want, joined)
		}
	}
	var path string
	for _, e := range env {
		if strings.HasPrefix(e, "NCS_RUNTIME_BUNDLE=") {
			path = strings.TrimPrefix(e, "NCS_RUNTIME_BUNDLE=")
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "bundle bytes" {
		t.Errorf("the staged bundle holds %q", b)
	}
	cleanup()
	if _, err := os.Stat(path); err == nil {
		t.Error("cleanup left the bundle behind")
	}
}

func TestStageRefusesAFileThatDoesNotMatchTheSignedChecksumAndLeavesNothing(t *testing.T) {
	srv := server(t, map[string]string{BundleFile: "tampered", ContainerdFile: "containerd bytes", CrictlFile: "crictl bytes"})
	defer srv.Close()
	dir := t.TempDir()
	_, _, err := Stager{BaseURL: srv.URL, Dir: dir}.Stage(context.Background(), params("bundle bytes", "containerd bytes"))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a failed stage left %d entries behind", len(entries))
	}
}

func TestStageFailsWhenThePlatformHasNoFile(t *testing.T) {
	srv := server(t, map[string]string{})
	defer srv.Close()
	if _, _, err := (Stager{BaseURL: srv.URL, Dir: t.TempDir()}).Stage(context.Background(), params("a", "b")); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseParamsIsStrict(t *testing.T) {
	good := map[string]any{"bundleVersion": "v4", "bundleSha256": strings.Repeat("b", 64), "containerdVersion": "2.2.10", "containerdSha256": strings.Repeat("c", 64), "crictlVersion": "1.37.0", "crictlSha256": strings.Repeat("e", 64), "rootFingerprint": strings.Repeat("d", 64)}
	if _, err := ParseParams(good); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bundleVersion", "bundleSha256", "containerdSha256", "rootFingerprint"} {
		bad := map[string]any{}
		for k, v := range good {
			bad[k] = v
		}
		bad[key] = "../etc"
		if _, err := ParseParams(bad); err == nil {
			t.Errorf("a bad %s was accepted", key)
		}
	}
}

func TestStageFetchesTheFilesNamedForThePinnedArchitecture(t *testing.T) {
	srv := server(t, map[string]string{"oci-saas-bundle-arm64.tar.gz": "bundle bytes", "containerd-arm64.tar.gz": "containerd bytes", "crictl-arm64.tar.gz": "crictl bytes"})
	defer srv.Close()
	old := hostArch
	hostArch = func() string { return "arm64" }
	defer func() { hostArch = old }()
	p := params("bundle bytes", "containerd bytes")
	p.Arch = "arm64"
	_, cleanup, err := Stager{BaseURL: srv.URL, Dir: t.TempDir()}.Stage(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
}

func TestStageRefusesFilesBuiltForAnotherArchitecture(t *testing.T) {
	srv := server(t, map[string]string{"oci-saas-bundle-arm64.tar.gz": "bundle bytes", "containerd-arm64.tar.gz": "containerd bytes", "crictl-arm64.tar.gz": "crictl bytes"})
	defer srv.Close()
	old := hostArch
	hostArch = func() string { return "amd64" }
	defer func() { hostArch = old }()
	dir := t.TempDir()
	p := params("bundle bytes", "containerd bytes")
	p.Arch = "arm64"
	if _, _, err := (Stager{BaseURL: srv.URL, Dir: dir}).Stage(context.Background(), p); err == nil || !strings.Contains(err.Error(), "for arm64 and this machine is amd64") {
		t.Fatalf("want a refusal naming both architectures, got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused stage left %d entries behind", len(entries))
	}
}

func TestParseParamsKnowsArchitecturesAndStaysCompatible(t *testing.T) {
	raw := map[string]any{"bundleVersion": "v4", "bundleSha256": sum("a"), "containerdVersion": "2.2.10", "containerdSha256": sum("b"), "crictlVersion": "1.37.0", "crictlSha256": sum("c"), "rootFingerprint": strings.Repeat("a", 64)}
	if p, err := ParseParams(raw); err != nil || p.Arch != "" {
		t.Fatalf("the platform that predates architectures must still parse: %v %q", err, p.Arch)
	}
	raw["arch"] = "arm64"
	if p, err := ParseParams(raw); err != nil || p.Arch != "arm64" {
		t.Fatalf("arm64 must parse: %v %q", err, p.Arch)
	}
	raw["arch"] = "riscv64"
	if _, err := ParseParams(raw); err == nil {
		t.Fatal("an architecture the agent does not know must be refused")
	}
	if named(ContainerdFile, "") != "containerd.tar.gz" || named(ContainerdFile, "arm64") != "containerd-arm64.tar.gz" {
		t.Error("file naming")
	}
}
