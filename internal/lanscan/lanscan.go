// Package lanscan asks which addresses of a private range answer on this machine's own network. It is how the platform learns
// that something it knows nothing about already uses an address in the range it allocates from. It sends one ARP request (or,
// without arping, one ping) per address, from the interface of the default route, and changes nothing.
package lanscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxAddresses bounds one scan.
const MaxAddresses = 256

type Answer struct {
	Address string `json:"address"`
	MAC     string `json:"mac,omitempty"`
}

type Result struct {
	From     string   `json:"from"`
	Method   string   `json:"method"` // arping, or ping when arping is not installed
	Scanned  int      `json:"scanned"`
	Answered []Answer `json:"answered"`
}

// Scanner holds what a scan needs from the system; every part can be replaced in tests.
type Scanner struct {
	// Run runs a command and returns what it printed; a non-nil error means it failed (or exited non-zero).
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// Has says whether a command is installed.
	Has func(name string) bool
	// Interface is the interface of the default route.
	Interface func() (string, error)
	// Own are this machine's own IPv4 addresses (not probed).
	Own func() map[string]bool
}

func realRun(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Real is a Scanner on this machine.
func Real() Scanner {
	return Scanner{
		Run: realRun,
		Has: func(name string) bool { _, err := exec.LookPath(name); return err == nil },
		Interface: func() (string, error) {
			raw, err := os.ReadFile("/proc/net/route")
			if err != nil {
				return "", err
			}
			return DefaultInterface(string(raw))
		},
		Own: func() map[string]bool {
			own := map[string]bool{}
			addrs, _ := net.InterfaceAddrs()
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
					own[ipn.IP.String()] = true
				}
			}
			return own
		},
	}
}

// DefaultInterface reads /proc/net/route: the interface of the route to 0.0.0.0.
func DefaultInterface(table string) (string, error) {
	for _, line := range strings.Split(table, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return f[0], nil
		}
	}
	return "", errors.New("the machine has no default route, so there is no network to scan from")
}

var ifaceName = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,15}$`)

// Addresses lists first..last, refusing anything that is not a private IPv4 range of at most MaxAddresses.
func Addresses(first, last string) ([]string, error) {
	a, b := net.ParseIP(first).To4(), net.ParseIP(last).To4()
	if a == nil || b == nil || strings.Contains(first, ":") || strings.Contains(last, ":") {
		return nil, errors.New("first and last must be IPv4 addresses")
	}
	if !a.IsPrivate() || !b.IsPrivate() {
		return nil, errors.New("a scan only looks at private addresses")
	}
	n := func(ip net.IP) uint32 {
		return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	}
	if n(b) < n(a) {
		return nil, errors.New("last is before first")
	}
	if n(b)-n(a)+1 > MaxAddresses {
		return nil, fmt.Errorf("a scan covers at most %d addresses", MaxAddresses)
	}
	var out []string
	for v := n(a); v <= n(b); v++ {
		out = append(out, net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String())
	}
	return out, nil
}

var (
	arpingMAC = regexp.MustCompile(`\[([0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5})\]`)
	neighMAC  = regexp.MustCompile(`lladdr\s+([0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5})`)
)

// Scan probes every address from first to last.
func (s Scanner) Scan(ctx context.Context, first, last string) (Result, error) {
	addrs, err := Addresses(first, last)
	if err != nil {
		return Result{}, err
	}
	iface, err := s.Interface()
	if err != nil {
		return Result{}, err
	}
	if !ifaceName.MatchString(iface) {
		return Result{}, fmt.Errorf("odd interface name %q", iface)
	}
	method := "ping"
	if s.Has("arping") {
		method = "arping"
	}
	own := s.Own()

	var mu sync.Mutex
	var answered []Answer
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	scanned := 0
	for _, addr := range addrs {
		if own[addr] {
			continue
		}
		scanned++
		wg.Add(1)
		sem <- struct{}{}
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			probe, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			var mac string
			var ok bool
			if method == "arping" {
				out, err := s.Run(probe, "arping", "-c", "1", "-w", "1", "-I", iface, addr)
				ok = err == nil
				if m := arpingMAC.FindStringSubmatch(out); m != nil {
					mac = strings.ToLower(m[1])
				}
			} else {
				_, err := s.Run(probe, "ping", "-c", "1", "-W", "1", "-I", iface, addr)
				ok = err == nil
				if ok {
					if out, err := s.Run(probe, "ip", "neigh", "show", addr); err == nil {
						if m := neighMAC.FindStringSubmatch(out); m != nil {
							mac = strings.ToLower(m[1])
						}
					}
				}
			}
			if ok {
				mu.Lock()
				answered = append(answered, Answer{Address: addr, MAC: mac})
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()
	sort.Slice(answered, func(i, j int) bool { return lessIP(answered[i].Address, answered[j].Address) })
	if answered == nil {
		answered = []Answer{}
	}
	return Result{From: iface, Method: method, Scanned: scanned, Answered: answered}, nil
}

func lessIP(a, b string) bool {
	x, y := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	return bytes.Compare(x, y) < 0
}
