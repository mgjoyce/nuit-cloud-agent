// Package hostname implements the set-hostname task: it changes the machine's hostname, /etc/hostname and the 127.0.1.1
// line of /etc/hosts, and nothing else. Commands and file locations are injectable so tests touch no real machine.
package hostname

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// label is the platform's own rule (catalog.ts HOSTNAME_LABEL): one DNS label, no dots, no leading or trailing dash, 1-63.
var label = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Validate re-checks what the platform signed: exactly one key, hostname, a string; it is trimmed and lower-cased and must be
// one DNS label. It returns the normalised name.
func Validate(params map[string]any) (string, error) {
	for k := range params {
		if k != "hostname" {
			return "", errors.New("set-hostname takes only a hostname")
		}
	}
	raw, ok := params["hostname"].(string)
	if !ok {
		return "", errors.New("set-hostname needs a hostname (a string)")
	}
	name := strings.ToLower(strings.TrimSpace(raw))
	if !label.MatchString(name) {
		return "", errors.New("hostname must be one DNS label: 1 to 63 lower-case letters, digits and dashes, not starting or ending with a dash")
	}
	return name, nil
}

// Result is what the task reports. Nothing in it is secret.
type Result struct {
	Previous string `json:"previous"`
	Hostname string `json:"hostname"`
	// Outcome is "changed" or "unchanged".
	Outcome string `json:"outcome"`
	// Method is "hostnamectl", "hostname-file" (the fallback) or "none" when nothing was changed.
	Method string `json:"method"`
}

// Setter does the work.
type Setter struct {
	// Command runs a program and returns its combined output; nil runs the real one.
	Command      func(ctx context.Context, name string, args ...string) (string, error)
	HostnamePath string // default /etc/hostname
	HostsPath    string // default /etc/hosts
}

func (s Setter) run(ctx context.Context, name string, args ...string) (string, error) {
	if s.Command != nil {
		return s.Command(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func (s Setter) paths() (string, string) {
	h, hosts := s.HostnamePath, s.HostsPath
	if h == "" {
		h = "/etc/hostname"
	}
	if hosts == "" {
		hosts = "/etc/hosts"
	}
	return h, hosts
}

// Set makes name the machine's hostname. It is idempotent: when the running hostname, /etc/hostname and /etc/hosts already
// say it, nothing is written and no command that changes anything is run.
func (s Setter) Set(ctx context.Context, name string) (Result, error) {
	if !label.MatchString(name) {
		return Result{}, errors.New("not a valid hostname")
	}
	hostnamePath, hostsPath := s.paths()

	running := ""
	if out, err := s.run(ctx, "hostname"); err == nil {
		running = strings.TrimSpace(out)
	}
	fileName := ""
	if b, err := os.ReadFile(hostnamePath); err == nil {
		fileName = strings.TrimSpace(string(b))
	}
	previous := running
	if previous == "" {
		previous = fileName
	}

	hosts, hostsMode, hostsOwner, err := readHosts(hostsPath)
	if err != nil {
		return Result{}, err
	}
	newHosts := RewriteHosts(hosts, name, running, fileName)

	if running == name && fileName == name && newHosts == hosts {
		return Result{Previous: previous, Hostname: name, Outcome: "unchanged", Method: "none"}, nil
	}

	method := "hostnamectl"
	if _, ctlErr := s.run(ctx, "hostnamectl", "set-hostname", name); ctlErr != nil {
		method = "hostname-file"
		if err := writeAtomic(hostnamePath, []byte(name+"\n"), 0o644, nil); err != nil {
			return Result{}, fmt.Errorf("hostnamectl failed (%v) and %s could not be written: %w", ctlErr, hostnamePath, err)
		}
		if out, err := s.run(ctx, "hostname", name); err != nil {
			return Result{}, fmt.Errorf("hostnamectl failed (%v) and `hostname` failed too: %v %s", ctlErr, err, strings.TrimSpace(out))
		}
	}
	if newHosts != hosts {
		if err := writeAtomic(hostsPath, []byte(newHosts), hostsMode, hostsOwner); err != nil {
			return Result{}, fmt.Errorf("the hostname is now %s but %s could not be updated: %w", name, hostsPath, err)
		}
	}
	return Result{Previous: previous, Hostname: name, Outcome: "changed", Method: method}, nil
}

type owner struct{ uid, gid int }

func readHosts(path string) (string, os.FileMode, *owner, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0o644, nil, nil
	}
	if err != nil {
		return "", 0, nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, nil, err
	}
	var o *owner
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		o = &owner{int(sys.Uid), int(sys.Gid)}
	}
	return string(b), st.Mode().Perm(), o, nil
}

// RewriteHosts returns hosts with the first 127.0.1.1 line pointing at name (aliases other than the old hostnames kept, any
// trailing comment kept), or with `127.0.1.1<TAB>name` appended when there is none. Every other line is returned unchanged.
func RewriteHosts(hosts, name string, olds ...string) string {
	lines := strings.SplitAfter(hosts, "\n")
	for i, line := range lines {
		body := strings.TrimRight(line, "\n")
		eol := line[len(body):]
		code, comment := body, ""
		if at := strings.Index(body, "#"); at >= 0 {
			code, comment = body[:at], body[at:]
		}
		fields := strings.Fields(code)
		if len(fields) == 0 || fields[0] != "127.0.1.1" {
			continue
		}
		names := []string{name}
		for _, a := range fields[1:] {
			if a == name || contains(olds, a) {
				continue
			}
			names = append(names, a)
		}
		// keep the original indentation and the spacing after the address
		start := strings.Index(code, "127.0.1.1")
		sep := "\t"
		rest := code[start+len("127.0.1.1"):]
		if ws := rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))]; ws != "" {
			sep = ws
		}
		out := code[:start] + "127.0.1.1" + sep + strings.Join(names, " ")
		if comment != "" {
			out += " " + comment
		}
		lines[i] = out + eol
		return strings.Join(lines, "")
	}
	if hosts != "" && !strings.HasSuffix(hosts, "\n") {
		hosts += "\n"
	}
	return hosts + "127.0.1.1\t" + name + "\n"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x != "" && x == s {
			return true
		}
	}
	return false
}

// writeAtomic writes a temporary file beside path (same directory, so the rename is atomic), gives it the mode and owner,
// and renames it into place.
func writeAtomic(path string, data []byte, mode os.FileMode, o *owner) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".nuit-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if o != nil {
		if err := f.Chown(o.uid, o.gid); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
