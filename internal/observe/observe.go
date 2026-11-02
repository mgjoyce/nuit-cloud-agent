// Package observe collects the small read-only facts the platform asks the agent to attach to its check-in (ADR 0001, amendment: observations).
//
// It is deliberately not a collector: nothing runs between check-ins, nothing is sent that the platform did not ask for, and the kinds are a closed list
// compiled in here. A read is made just before a call and is passive: the interface list and the neighbour table, never a probe.
package observe

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Kind names.
const Network = "network"

// Limits (the platform enforces the same ones again).
const (
	maxInterfaces = 32
	maxAddresses  = 16
	maxNeighbours = 512
)

// Interface is one network interface of this machine.
type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac,omitempty"`
	Addresses []string `json:"addresses"`
}

// Neighbour is an address this machine has recently heard from, as its neighbour (ARP) table says.
type Neighbour struct {
	Address   string `json:"address"`
	MAC       string `json:"mac"`
	Interface string `json:"interface,omitempty"`
	State     string `json:"state,omitempty"`
}

// NetworkFacts is the `network` observation.
type NetworkFacts struct {
	Interfaces []Interface `json:"interfaces"`
	Neighbours []Neighbour `json:"neighbours"`
}

// Collector reads the facts; every part can be replaced in tests.
type Collector struct {
	// Run runs a command and returns what it printed; a non-nil error means it failed.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// Interfaces lists this machine's interfaces.
	Interfaces func() []Interface
}

func realRun(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

// Real is a Collector on this machine.
func Real() Collector {
	return Collector{Run: realRun, Interfaces: realInterfaces}
}

func realInterfaces() []Interface {
	var out []Interface
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		var addrs []string
		list, _ := ifc.Addrs()
		for _, a := range list {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				addrs = append(addrs, ipn.IP.String())
			}
		}
		out = append(out, Interface{Name: ifc.Name, MAC: strings.ToLower(ifc.HardwareAddr.String()), Addresses: addrs})
	}
	return out
}

// Known says whether this agent can collect a kind. A kind the platform asks for that is not here is simply not sent.
func (c Collector) Known(kind string) bool { return kind == Network }

// Collect reads one kind. The result is what is sent as the observation's `data`.
func (c Collector) Collect(ctx context.Context, kind string) (any, error) {
	if kind != Network {
		return nil, nil
	}
	return c.network(ctx), nil
}

func (c Collector) network(ctx context.Context) NetworkFacts {
	facts := NetworkFacts{Interfaces: []Interface{}, Neighbours: []Neighbour{}}
	if c.Interfaces != nil {
		for _, ifc := range c.Interfaces() {
			if len(facts.Interfaces) >= maxInterfaces {
				break
			}
			if len(ifc.Addresses) > maxAddresses {
				ifc.Addresses = ifc.Addresses[:maxAddresses]
			}
			if ifc.Addresses == nil {
				ifc.Addresses = []string{}
			}
			facts.Interfaces = append(facts.Interfaces, ifc)
		}
	}
	if c.Run != nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// A machine without `ip` still reports its interfaces; its neighbours are simply not known.
		if out, err := c.Run(ctx, "ip", "-4", "neigh", "show"); err == nil {
			facts.Neighbours = ParseNeighbours(out)
		}
	}
	return facts
}

var macRe = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// ParseNeighbours reads `ip -4 neigh show`: `10.10.10.1 dev eth0 lladdr aa:bb:cc:dd:ee:ff REACHABLE`. An entry with no hardware address, or one that
// failed or is incomplete, is a neighbour that did not answer and is left out.
func ParseNeighbours(output string) []Neighbour {
	out := []Neighbour{}
	for _, line := range strings.Split(output, "\n") {
		w := strings.Fields(line)
		if len(w) < 1 {
			continue
		}
		ip := net.ParseIP(w[0])
		if ip == nil || ip.To4() == nil {
			continue
		}
		n := Neighbour{Address: ip.To4().String()}
		for i := 0; i+1 < len(w); i++ {
			switch w[i] {
			case "dev":
				n.Interface = w[i+1]
			case "lladdr":
				n.MAC = strings.ToLower(w[i+1])
			}
		}
		if !macRe.MatchString(n.MAC) {
			continue
		}
		if last := w[len(w)-1]; last == strings.ToUpper(last) && last != "" && !strings.ContainsAny(last, ":.0123456789") {
			n.State = last
		}
		if n.State == "FAILED" || n.State == "INCOMPLETE" {
			continue
		}
		out = append(out, n)
		if len(out) >= maxNeighbours {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}
