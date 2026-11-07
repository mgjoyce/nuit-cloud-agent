package controlplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheRealBundlesBuiltByThePlatformAreAccepted runs the bundles the platform builds (deploy/control-plane in the platform repository) through
// the same code Install uses: the checksum check, then the hardened extractor. Skipped unless CONTROL_PLANE_BUNDLE_DIR names a directory holding
// control-plane.json and control-plane-<arch>.tar.gz files:
//
//	CONTROL_PLANE_BUNDLE_DIR=<platform>/deploy/control-plane go test -count=1 -run RealBundles -v ./internal/controlplane
func TestTheRealBundlesBuiltByThePlatformAreAccepted(t *testing.T) {
	dir := os.Getenv("CONTROL_PLANE_BUNDLE_DIR")
	if dir == "" {
		t.Skip("CONTROL_PLANE_BUNDLE_DIR is not set")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, arch := range []string{"amd64", "arm64"} {
		b := Bundle{Arch: arch}
		data, err := os.ReadFile(filepath.Join(dir, b.File()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		found++
		t.Run(arch, func(t *testing.T) {
			pin, ok := pins[arch]
			if !ok {
				t.Fatalf("control-plane.json has no %s entry", arch)
			}
			b := Bundle{Version: pin.Version, Arch: arch, SHA256: pin.SHA256}
			if sum(data) != b.SHA256 {
				t.Fatalf("the file's sha256 is %s, control-plane.json says %s", sum(data), b.SHA256)
			}
			f := &memFetcher{files: map[string][]byte{b.File(): data}}
			root, stage := filepath.Join(t.TempDir(), "opt"), filepath.Join(t.TempDir(), "stage")
			if err := Install(context.Background(), f, b, root, stage); err != nil {
				t.Fatalf("the bundle was refused: %v", err)
			}
			target := filepath.Join(root, b.Version)
			// every member of the archive is on disk with the mode the contract gives it
			members := listMembers(t, data)
			if len(members) == 0 {
				t.Fatal("no members")
			}
			for _, m := range members {
				st, err := os.Lstat(filepath.Join(target, filepath.FromSlash(m.name)))
				if err != nil {
					t.Errorf("%s is missing: %v", m.name, err)
					continue
				}
				want := os.FileMode(0o644)
				switch {
				case m.dir:
					want = 0o755
					if !st.IsDir() {
						t.Errorf("%s is not a directory", m.name)
					}
				case m.name == "ofelia" || m.name == "probe/probe.sh":
					want = 0o755
				}
				if st.Mode().Perm() != want || (!m.dir && !st.Mode().IsRegular()) {
					t.Errorf("%s has mode %v (want %v, regular=%v)", m.name, st.Mode(), want, st.Mode().IsRegular())
				}
			}
			ofelia, err := os.Lstat(filepath.Join(target, "ofelia"))
			if err != nil || !ofelia.Mode().IsRegular() || ofelia.Mode().Perm()&0o111 != 0o111 || ofelia.Size() < 1<<20 {
				t.Errorf("ofelia is not an executable regular file: %v %v", ofelia, err)
			}
			version, _ := os.ReadFile(filepath.Join(target, "VERSION"))
			if strings.TrimSpace(string(version)) != b.Version {
				t.Errorf("VERSION is %q, the pin says %q", version, b.Version)
			}
			if got, _ := os.ReadFile(filepath.Join(target, ShaFile)); strings.TrimSpace(string(got)) != b.SHA256 {
				t.Errorf("the checksum was not recorded: %q", got)
			}
			for _, name := range required {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(name))); err != nil {
					t.Errorf("required %s is missing", name)
				}
			}
			t.Logf("%s %s: accepted, %d members, ofelia %d bytes", arch, b.Version, len(members), ofelia.Size())

			// one byte changed: the checksum refuses it before anything is unpacked
			bad := append([]byte(nil), data...)
			bad[len(bad)/2] ^= 0x01
			fb := &memFetcher{files: map[string][]byte{b.File(): bad}}
			root2 := filepath.Join(t.TempDir(), "opt")
			err = Install(context.Background(), fb, b, root2, stage)
			if err == nil || !strings.Contains(err.Error(), "checksum") {
				t.Errorf("a changed byte was not refused by the checksum: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(root2, b.Version)); !os.IsNotExist(statErr) {
				t.Error("a bundle with a changed byte was unpacked")
			}
		})
	}
	if found == 0 {
		t.Fatalf("no control-plane-<arch>.tar.gz in %s", dir)
	}
}

type member struct {
	name string
	dir  bool
}

func listMembers(t *testing.T, data []byte) []member {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out []member
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, member{name: strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/"), dir: h.Typeflag == tar.TypeDir})
	}
}
