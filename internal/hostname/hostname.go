// Package hostname implements the set-hostname task: it changes the machine's hostname, /etc/hostname and the one line of
// /etc/hosts that names this machine, and nothing else. Commands and file locations are injectable so tests touch no real
// machine.
package hostname

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// label is the platform's own rule (catalog.ts HOSTNAME_LABEL): one DNS label, no dots, no leading or trailing dash, 1-63.
var label = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// dotted is a strictly dotted IPv4 address (no leading zeros).
var dotted = regexp.MustCompile(`^((25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])$`)

// Marker is the comment that makes a line of /etc/hosts this agent's own.
const Marker = "# nuit-cloud"

// Validate re-checks what the platform signed: the keys hostname (required) and address (optional) and no others. The
// hostname is trimmed and lower-cased and must be one DNS label; the address must be a dotted IPv4 that is not 0.0.0.0,
// loopback, link-local or multicast and above. It returns the normalised name and the address ("" when none was given).
func Validate(params map[string]any) (string, string, error) {
	for k := range params {
		if k != "hostname" && k != "address" {
			return "", "", errors.New("set-hostname takes only a hostname and an address")
		}
	}
	raw, ok := params["hostname"].(string)
	if !ok {
		return "", "", errors.New("set-hostname needs a hostname (a string)")
	}
	name := strings.ToLower(strings.TrimSpace(raw))
	if !label.MatchString(name) {
		return "", "", errors.New("hostname must be one DNS label: 1 to 63 lower-case letters, digits and dashes, not starting or ending with a dash")
	}
	rawAddr, has := params["address"]
	if !has {
		return name, "", nil
	}
	address, ok := rawAddr.(string)
	if !ok || !usableAddress(address) {
		return "", "", errors.New("address must be a unicast IPv4 address (not 0.0.0.0, loopback, link-local or multicast)")
	}
	return name, address, nil
}

func usableAddress(a string) bool {
	if !dotted.MatchString(a) {
		return false
	}
	ip := net.ParseIP(a).To4()
	if ip == nil || ip.IsUnspecified() || ip[0] == 127 || (ip[0] == 169 && ip[1] == 254) || ip[0] >= 224 {
		return false
	}
	return true
}

// Result is what the task reports. Nothing in it is secret.
type Result struct {
	Previous string `json:"previous"`
	Hostname string `json:"hostname"`
	// Outcome is "changed" or "unchanged".
	Outcome string `json:"outcome"`
	// Method is "hostnamectl", "hostname-file" (the fallback), "hosts-only" (the name was already right) or "none".
	Method string `json:"method"`
	// Address is the address /etc/hosts maps the name to, or "" when it maps to loopback.
	Address string `json:"address"`
	// HostsLine is "address" or "loopback".
	HostsLine string `json:"hostsLine"`
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

// Set makes name the machine's hostname, with /etc/hosts mapping it to address (or to 127.0.1.1 when address is empty). It
// is idempotent: when the running hostname, /etc/hostname and /etc/hosts are already exactly as this would leave them, nothing
// is written and no command that changes anything is run. When only the address differs, only /etc/hosts is rewritten.
func (s Setter) Set(ctx context.Context, name, address string) (Result, error) {
	if !label.MatchString(name) {
		return Result{}, errors.New("not a valid hostname")
	}
	if address != "" && !usableAddress(address) {
		return Result{}, errors.New("not a usable address")
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
	newHosts := RewriteHosts(hosts, name, address, running, fileName)
	line := "loopback"
	if address != "" {
		line = "address"
	}
	res := Result{Previous: previous, Hostname: name, Outcome: "changed", Method: "hosts-only", Address: address, HostsLine: line}

	nameRight := running == name && fileName == name
	if nameRight && newHosts == hosts {
		res.Outcome, res.Method = "unchanged", "none"
		return res, nil
	}

	if !nameRight {
		res.Method = "hostnamectl"
		if _, ctlErr := s.run(ctx, "hostnamectl", "set-hostname", name); ctlErr != nil {
			res.Method = "hostname-file"
			if err := writeAtomic(hostnamePath, []byte(name+"\n"), 0o644, nil); err != nil {
				return Result{}, fmt.Errorf("hostnamectl failed (%v) and %s could not be written: %w", ctlErr, hostnamePath, err)
			}
			if out, err := s.run(ctx, "hostname", name); err != nil {
				return Result{}, fmt.Errorf("hostnamectl failed (%v) and `hostname` failed too: %v %s", ctlErr, err, strings.TrimSpace(out))
			}
		}
	}
	if newHosts != hosts {
		if err := writeAtomic(hostsPath, []byte(newHosts), hostsMode, hostsOwner); err != nil {
			return Result{}, fmt.Errorf("the hostname is now %s but %s could not be updated: %w", name, hostsPath, err)
		}
	}
	return res, nil
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

type hostsLine struct {
	indent, addr, sep string // leading space, first field, whitespace after it
	fields            []string
	comment, eol      string
}

func parseLine(line string) (hostsLine, bool) {
	body := strings.TrimRight(line, "\n")
	l := hostsLine{eol: line[len(body):]}
	code := body
	if at := strings.Index(body, "#"); at >= 0 {
		code, l.comment = body[:at], body[at:]
	}
	l.fields = strings.Fields(code)
	if len(l.fields) == 0 {
		return l, false
	}
	l.addr = l.fields[0]
	start := strings.Index(code, l.addr)
	l.indent = code[:start]
	rest := code[start+len(l.addr):]
	l.sep = rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))]
	return l, true
}

// RewriteHosts changes at most one line of hosts so that it maps name (aliases other than the old hostnames kept). The line
// changed is this agent's own (an address, comment exactly "# nuit-cloud") if there is one, else the first `127.0.1.1` line,
// else one is appended.
//
// With an address the line becomes `<address> <name> [aliases] # nuit-cloud` (appended: `<address><TAB><name><TAB># nuit-cloud`).
// Without one it becomes `127.0.1.1 <name> [aliases]`, without the marker (appended: `127.0.1.1<TAB><name>`).
//
// Indentation and the spacing after the address are kept. A trailing comment on a rewritten line is replaced (by the marker,
// or by nothing), since the marker is the whole comment that identifies the line. Every other line is returned unchanged.
func RewriteHosts(hosts, name, address string, olds ...string) string {
	lines := strings.SplitAfter(hosts, "\n")
	ours, loop := -1, -1
	for i, raw := range lines {
		l, ok := parseLine(raw)
		if !ok {
			continue
		}
		if ours < 0 && net.ParseIP(l.addr) != nil && strings.TrimSpace(l.comment) == Marker {
			ours = i
		}
		if loop < 0 && l.addr == "127.0.1.1" {
			loop = i
		}
	}
	target := ours
	if target < 0 {
		target = loop
	}
	if target >= 0 {
		l, _ := parseLine(lines[target])
		names := []string{name}
		for _, a := range l.fields[1:] {
			if a == name || contains(olds, a) {
				continue
			}
			names = append(names, a)
		}
		sep := l.sep
		if sep == "" {
			sep = "\t"
		}
		addr, comment := "127.0.1.1", ""
		if address != "" {
			addr, comment = address, " "+Marker
		}
		lines[target] = l.indent + addr + sep + strings.Join(names, " ") + comment + l.eol
		return strings.Join(lines, "")
	}
	if hosts != "" && !strings.HasSuffix(hosts, "\n") {
		hosts += "\n"
	}
	if address != "" {
		return hosts + address + "\t" + name + "\t" + Marker + "\n"
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
