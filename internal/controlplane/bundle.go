package controlplane

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The control-plane bundle: one signed-by-checksum tarball the platform serves at /api/agent/download/control-plane-<arch>.tar.gz, with
// exactly this layout and nothing else:
//
//	VERSION                 text; equals the task's bundle.version
//	ofelia                  static executable for the architecture
//	scheduler/config.ini    the job configuration
//	scheduler/<name>.sh     the sweep scripts (report-job.sh and monitor-run.sh among them)
//	probe/probe.sh          the dyndns probe script
const (
	MaxBundleDownload = 64 << 20 // the download
	MaxBundleTotal    = 64 << 20 // all files together, once unpacked
	MaxBundleFile     = 32 << 20 // one file
	MaxBundleEntries  = 200
	// DefaultInstallDir is where each version is unpacked; `current` (a symlink the profile's scripts switch) names the one in use.
	DefaultInstallDir = "/opt/nuit-cloud/control-plane"
	// ShaFile is written into an unpacked version: the checksum of the bundle it came from.
	ShaFile = ".bundle-sha256"
)

var (
	versionRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	scriptName = regexp.MustCompile(`^scheduler/[a-z][a-z0-9-]{0,63}\.sh$`)
	required   = []string{"VERSION", "ofelia", "scheduler/config.ini", "scheduler/report-job.sh", "scheduler/monitor-run.sh", "probe/probe.sh"}
	executable = map[string]bool{"ofelia": true, "probe/probe.sh": true}
)

// Bundle is what a signed task says the bundle is.
type Bundle struct {
	Version string
	Arch    string
	SHA256  string
}

// File is the name the platform serves the bundle under.
func (b Bundle) File() string { return "control-plane-" + b.Arch + ".tar.gz" }

// Fetcher downloads one served file, checking its SHA-256 (runtimeinstall.Stager).
type Fetcher interface {
	Fetch(ctx context.Context, name, dst, want string, limit int64) (string, error)
}

func allowed(name string) bool {
	if name == "scheduler" || name == "probe" {
		return true
	}
	for _, r := range required {
		if r == name {
			return true
		}
	}
	return scriptName.MatchString(name)
}

// Extract reads a gzipped tarball and writes the bundle into dest (which must not exist) with a hardened extractor: only regular files
// and the two directories of the layout, no absolute path, `..`, link, device, setuid/setgid/sticky bit or duplicate, at most 200
// entries, 32 MB a file and 64 MB in all, and nothing that is not in the layout. Modes are made 0755 for directories and the two
// executables and 0644 for everything else, whatever the archive said. It insists on every required file and on VERSION being want.
func Extract(r io.Reader, dest, wantVersion string) error {
	// the decompressed stream is bounded too: a bomb is cut off here, not after it has filled the disk
	gz, err := gzip.NewReader(io.LimitReader(r, MaxBundleDownload))
	if err != nil {
		return errors.New("the bundle is not a gzip file")
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, MaxBundleTotal+int64(MaxBundleEntries)*2048))
	if err := os.Mkdir(dest, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	var total int64
	entries := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("the bundle is damaged or cut short")
		}
		entries++
		if entries > MaxBundleEntries {
			return fmt.Errorf("the bundle has more than %d entries", MaxBundleEntries)
		}
		name := strings.TrimPrefix(h.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("the bundle has an entry with an unsafe name (%q)", safeName(h.Name))
		}
		if seen[name] {
			return fmt.Errorf("the bundle names %q twice", name)
		}
		seen[name] = true
		if h.Mode&0o7000 != 0 {
			return fmt.Errorf("the bundle has an entry with a setuid, setgid or sticky bit (%q)", name)
		}
		if !allowed(name) {
			return fmt.Errorf("the bundle holds %q, which is not part of its layout", name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if name != "scheduler" && name != "probe" {
				return fmt.Errorf("the bundle has a directory %q that is not in its layout", name)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := os.Chmod(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if name == "scheduler" || name == "probe" {
				return fmt.Errorf("%q must be a directory", name)
			}
			if h.Size < 0 || h.Size > MaxBundleFile {
				return fmt.Errorf("%q is larger than %d MB", name, MaxBundleFile>>20)
			}
			total += h.Size
			if total > MaxBundleTotal {
				return fmt.Errorf("the bundle holds more than %d MB", MaxBundleTotal>>20)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if executable[name] {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, h.Size+1))
			cerr := f.Close()
			if err != nil || n != h.Size {
				return errors.New("the bundle is damaged or cut short")
			}
			if cerr != nil {
				return cerr
			}
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		default:
			// symlinks, hard links, devices, fifos, PAX global headers: nothing but files and directories is ever unpacked
			return fmt.Errorf("the bundle has an entry that is not a plain file or directory (%q)", name)
		}
	}
	for _, name := range required {
		if !seen[name] {
			return fmt.Errorf("the bundle lacks %s", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dest, "VERSION"))
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(raw)); got != wantVersion {
		return errors.New("the bundle's VERSION is not the version the task names")
	}
	return nil
}

// safeName keeps a refused name short and printable for the message.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		}
		if b.Len() >= 80 {
			break
		}
	}
	return b.String()
}

// Install makes sure the bundle the task names is unpacked at <root>/<version>: downloaded to stage, its checksum checked BEFORE anything
// is read from it, unpacked beside the target and moved into place. One already unpacked from the same checksum is left alone.
func Install(ctx context.Context, f Fetcher, b Bundle, root, stage string) error {
	if !versionRe.MatchString(b.Version) || !hexSHA256.MatchString(b.SHA256) {
		return errors.New("the bundle parameters are not usable")
	}
	target := filepath.Join(root, b.Version)
	if have, err := os.ReadFile(filepath.Join(target, ShaFile)); err == nil && strings.TrimSpace(string(have)) == b.SHA256 {
		return nil
	}
	for _, d := range []string{root, stage} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := os.Chmod(root, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(stage, "control-plane-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	archive, err := f.Fetch(ctx, b.File(), filepath.Join(work, b.File()), b.SHA256, MaxBundleDownload)
	if err != nil {
		return err
	}
	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.MkdirTemp(root, ".extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "v")
	if err := Extract(in, dest, b.Version); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, ShaFile), []byte(b.SHA256+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		// the same version from another build: set it aside, put the new one in, then remove the old
		if err := os.Rename(target, filepath.Join(tmp, "old")); err != nil {
			return err
		}
	}
	return os.Rename(dest, target)
}
