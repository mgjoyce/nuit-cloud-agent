package observe

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const neigh = `10.10.10.162 dev br0 lladdr 8E:FF:E6:55:1B:1E REACHABLE
10.10.10.250 dev br0  FAILED
10.10.10.1 dev br0 lladdr 74:ac:b9:11:22:33 PERMANENT
10.10.10.7 dev br0 lladdr zz:ac:b9:11:22:33 STALE
10.10.10.8 dev br0 INCOMPLETE
fe80::1 dev br0 lladdr 74:ac:b9:11:22:33 router STALE
junk
`

func TestParseNeighboursKeepsAnsweringIPv4EntriesWithAMACAndLowercasesIt(t *testing.T) {
	got := ParseNeighbours(neigh)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0] != (Neighbour{Address: "10.10.10.1", MAC: "74:ac:b9:11:22:33", Interface: "br0", State: "PERMANENT"}) {
		t.Errorf("%+v", got[0])
	}
	if got[1] != (Neighbour{Address: "10.10.10.162", MAC: "8e:ff:e6:55:1b:1e", Interface: "br0", State: "REACHABLE"}) {
		t.Errorf("%+v", got[1])
	}
}

func fake(out string, err error) Collector {
	return Collector{
		Run: func(ctx context.Context, name string, args ...string) (string, error) {
			if name != "ip" || strings.Join(args, " ") != "-4 neigh show" {
				panic("an observation may only run `ip -4 neigh show`")
			}
			return out, err
		},
		Interfaces: func() []Interface {
			return []Interface{{Name: "eth0", MAC: "aa:aa:aa:aa:aa:aa", Addresses: []string{"10.10.10.161"}}, {Name: "veth1"}}
		},
	}
}

func TestTheNetworkObservationHasTheInterfacesAndTheNeighbours(t *testing.T) {
	data, _, err := fake(neigh, nil).Collect(context.Background(), Network)
	if err != nil {
		t.Fatal(err)
	}
	f := data.(NetworkFacts)
	if len(f.Interfaces) != 2 || f.Interfaces[0].MAC != "aa:aa:aa:aa:aa:aa" || f.Interfaces[1].Addresses == nil {
		t.Errorf("interfaces %+v", f.Interfaces)
	}
	if len(f.Neighbours) != 2 {
		t.Errorf("neighbours %+v", f.Neighbours)
	}
}

func TestAMachineWithoutIPStillReportsItsInterfaces(t *testing.T) {
	data, _, _ := fake("", errors.New("not found")).Collect(context.Background(), Network)
	f := data.(NetworkFacts)
	if len(f.Interfaces) != 2 || f.Neighbours == nil || len(f.Neighbours) != 0 {
		t.Errorf("%+v", f)
	}
}

func TestOnlyTheNetworkKindIsKnown(t *testing.T) {
	c := fake("", nil)
	if !c.Known("network") || c.Known("cpu") || c.Known("") {
		t.Error("the catalog is closed")
	}
	if data, v, _ := c.Collect(context.Background(), "cpu"); data != nil || v != "" {
		t.Error("an unknown kind collects nothing")
	}
}

func TestTheReportIsBounded(t *testing.T) {
	var ifs []Interface
	for i := 0; i < 50; i++ {
		ifs = append(ifs, Interface{Name: "if", Addresses: make([]string, 40)})
	}
	c := Collector{Interfaces: func() []Interface { return ifs }}
	f := c.network(context.Background())
	if len(f.Interfaces) != maxInterfaces || len(f.Interfaces[0].Addresses) != maxAddresses {
		t.Errorf("%d interfaces, %d addresses", len(f.Interfaces), len(f.Interfaces[0].Addresses))
	}
	var b strings.Builder
	for i := 0; i < 700; i++ {
		b.WriteString("10.1." + string(rune('0'+i%10)) + ".1 dev e lladdr aa:aa:aa:aa:aa:aa REACHABLE\n")
	}
	if n := len(ParseNeighbours(b.String())); n > maxNeighbours {
		t.Errorf("%d neighbours", n)
	}
}

func TestTheVersionChangesWithTheFactNotWithTheNoise(t *testing.T) {
	_, v1, _ := fake(neigh, nil).Collect(context.Background(), Network)
	// the same neighbours in another order and in another state are the same fact
	shuffled := "10.10.10.1 dev br0 lladdr 74:ac:b9:11:22:33 STALE\n10.10.10.162 dev br0 lladdr 8e:ff:e6:55:1b:1e DELAY\n"
	_, v2, _ := fake(shuffled, nil).Collect(context.Background(), Network)
	if v1 != v2 || len(v1) != 16 {
		t.Errorf("%q vs %q", v1, v2)
	}
	// a new neighbour, or a changed MAC, is a different one
	_, v3, _ := fake(shuffled+"10.10.10.9 dev br0 lladdr aa:aa:aa:aa:aa:01 REACHABLE\n", nil).Collect(context.Background(), Network)
	_, v4, _ := fake(strings.Replace(shuffled, "74:ac:b9:11:22:33", "74:ac:b9:11:22:34", 1), nil).Collect(context.Background(), Network)
	if v3 == v2 || v4 == v2 || v3 == v4 {
		t.Errorf("%q %q %q", v2, v3, v4)
	}
}

func TestTheClockObservationSaysHowFarAheadThisMachineIsAndMovesInTenSecondSteps(t *testing.T) {
	c := Collector{Clock: func() (time.Duration, bool) { return -2 * time.Hour, true }}
	data, v, _ := c.Collect(context.Background(), Clock)
	if f := data.(ClockFacts); f.AheadSeconds != 7200 || v != "c720" {
		t.Errorf("%+v %q", data, v)
	}
	c.Clock = func() (time.Duration, bool) { return -7203 * time.Second, true }
	if _, v2, _ := c.Collect(context.Background(), Clock); v2 != "c720" {
		t.Errorf("a few seconds of drift changed the version: %q", v2)
	}
	c.Clock = func() (time.Duration, bool) { return 0, false }
	if data, v3, _ := c.Collect(context.Background(), Clock); data != nil || v3 != "" {
		t.Error("nothing is reported before the offset is known")
	}
	if !c.Known(Clock) {
		t.Error("clock is in the catalog")
	}
}
