package hostname

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fake struct {
	running   string
	ctlFails  bool
	hostFails bool
	calls     []string
}

func (f *fake) cmd(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	switch {
	case name == "hostname" && len(args) == 0:
		return f.running + "\n", nil
	case name == "hostnamectl":
		if f.ctlFails {
			return "Failed to connect to bus", errors.New("exit status 1")
		}
		f.running = args[1]
	case name == "hostname":
		if f.hostFails {
			return "", errors.New("exit status 1")
		}
		f.running = args[0]
	}
	return "", nil
}

func setup(t *testing.T, running, file, hosts string) (Setter, *fake, string, string) {
	t.Helper()
	dir := t.TempDir()
	hp, hosp := filepath.Join(dir, "hostname"), filepath.Join(dir, "hosts")
	if file != "" {
		os.WriteFile(hp, []byte(file+"\n"), 0o644)
	}
	if hosts != "<none>" {
		os.WriteFile(hosp, []byte(hosts), 0o640)
		os.Chmod(hosp, 0o640)
	}
	f := &fake{running: running}
	return Setter{Command: f.cmd, HostnamePath: hp, HostsPath: hosp}, f, hp, hosp
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const base = "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n"
const addr = "10.10.10.170"

func set(t *testing.T, s Setter, name, address string) Result {
	t.Helper()
	res, err := s.Set(context.Background(), name, address)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAValidRenameWithoutAnAddressUsesHostnamectlAndTheLoopbackLine(t *testing.T) {
	s, f, _, hosts := setup(t, "old", "old", base+"127.0.1.1\told\n10.0.0.5\tnas # keep\n")
	res := set(t, s, "new", "")
	if res != (Result{Previous: "old", Hostname: "new", Outcome: "changed", Method: "hostnamectl", HostsLine: "loopback"}) {
		t.Errorf("%+v", res)
	}
	if got := read(t, hosts); got != base+"127.0.1.1\tnew\n10.0.0.5\tnas # keep\n" {
		t.Errorf("%q", got)
	}
	if strings.Join(f.calls, "|") != "hostname|hostnamectl set-hostname new" {
		t.Errorf("%v", f.calls)
	}
	if st, _ := os.Stat(hosts); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode())
	}
}

func TestWithAnAddressALoopbackLineIsReplacedAndItsAliasesKept(t *testing.T) {
	s, _, _, hosts := setup(t, "old", "old", base+"127.0.1.1 old old.example.com extra  # note\n")
	res := set(t, s, "new", addr)
	if res.Address != addr || res.HostsLine != "address" || res.Outcome != "changed" || res.Method != "hostnamectl" {
		t.Errorf("%+v", res)
	}
	if got := read(t, hosts); got != base+addr+" new old.example.com extra # nuit-cloud\n" {
		t.Errorf("%q", got)
	}
}

func TestWithAnAddressAndNoLineOneIsAppended(t *testing.T) {
	s, _, _, hosts := setup(t, "old", "old", base)
	set(t, s, "new", addr)
	if got := read(t, hosts); got != base+addr+"\tnew\t# nuit-cloud\n" {
		t.Errorf("%q", got)
	}
	s, _, _, hosts = setup(t, "old", "old", "127.0.0.1 localhost") // no final newline
	set(t, s, "new", addr)
	if got := read(t, hosts); got != "127.0.0.1 localhost\n"+addr+"\tnew\t# nuit-cloud\n" {
		t.Errorf("%q", got)
	}
}

func TestOurOwnLineIsRewrittenInPlaceAndALoopbackLineLeftAlone(t *testing.T) {
	in := base + "127.0.1.1\tother\n10.10.10.9 old alias # nuit-cloud\n"
	s, _, _, hosts := setup(t, "old", "old", in)
	set(t, s, "new", addr)
	if got := read(t, hosts); got != base+"127.0.1.1\tother\n"+addr+" new alias # nuit-cloud\n" {
		t.Errorf("%q", got)
	}
}

func TestDroppingTheAddressPutsTheNameBackOnLoopbackWithoutTheMarker(t *testing.T) {
	s, f, _, hosts := setup(t, "box", "box", base+addr+"\tbox alias # nuit-cloud\nfe00::0 ip6-localnet\n")
	res := set(t, s, "box", "")
	if res.Outcome != "changed" || res.Method != "hosts-only" || res.HostsLine != "loopback" || res.Address != "" {
		t.Errorf("%+v", res)
	}
	if got := read(t, hosts); got != base+"127.0.1.1\tbox alias\nfe00::0 ip6-localnet\n" {
		t.Errorf("%q", got)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "hostnamectl") {
			t.Errorf("hostnamectl was called: %v", f.calls)
		}
	}
}

func TestAChangeOfAddressAloneTouchesOnlyHosts(t *testing.T) {
	s, f, hp, hosts := setup(t, "box", "box", base+addr+"\tbox # nuit-cloud\n")
	before := read(t, hp)
	res := set(t, s, "box", "10.10.10.171")
	if res.Outcome != "changed" || res.Method != "hosts-only" {
		t.Errorf("%+v", res)
	}
	if got := read(t, hosts); got != base+"10.10.10.171\tbox # nuit-cloud\n" {
		t.Errorf("%q", got)
	}
	if strings.Join(f.calls, "|") != "hostname" || read(t, hp) != before {
		t.Errorf("the name was touched: %v", f.calls)
	}
}

func TestTwoRunsInARowAreANoOpTheSecondTime(t *testing.T) {
	for _, address := range []string{"", addr} {
		s, f, _, hosts := setup(t, "old", "old", base+"127.0.1.1\told\n")
		set(t, s, "new", address)
		after := read(t, hosts)
		f.calls = nil
		// the fake's hostnamectl does not write /etc/hostname, as the real one does; do it so the second run sees the same
		os.WriteFile(s.HostnamePath, []byte("new\n"), 0o644)
		res := set(t, s, "new", address)
		if res.Outcome != "unchanged" || res.Method != "none" || read(t, hosts) != after || strings.Join(f.calls, "|") != "hostname" {
			t.Errorf("address %q: %+v %v", address, res, f.calls)
		}
	}
}

func TestRunningNameRightButHostsStaleIsStillRepaired(t *testing.T) {
	s, _, _, hosts := setup(t, "new", "new", base+"127.0.1.1\told\n")
	res := set(t, s, "new", "")
	if res.Outcome != "changed" {
		t.Fatalf("%+v", res)
	}
	// the name that was replaced earlier is no longer known, so it stays as an alias (never dropped by guesswork)
	if read(t, hosts) != base+"127.0.1.1\tnew old\n" {
		t.Errorf("%q", read(t, hosts))
	}
}

func TestRewriteHostsOddLayouts(t *testing.T) {
	cases := []struct{ in, address, want string }{
		{"127.0.0.1 localhost", "", "127.0.0.1 localhost\n127.0.1.1\tnew\n"},
		{"127.0.0.1 localhost\n  127.0.1.1   old", "", "127.0.0.1 localhost\n  127.0.1.1   new"},
		// a commented-out 127.0.1.1 is not a line; blanks and a second 127.0.1.1 are left alone
		{"#127.0.1.1 old\n\n127.0.1.1 old\n127.0.1.1 old\n", "", "#127.0.1.1 old\n\n127.0.1.1 new\n127.0.1.1 old\n"},
		{"127.0.1.10 old\n", "", "127.0.1.10 old\n127.0.1.1\tnew\n"},
		{"", "", "127.0.1.1\tnew\n"},
		{"", addr, addr + "\tnew\t# nuit-cloud\n"},
		// with an address: the commented line and 127.0.1.10 are not ours to replace
		{"#127.0.1.1 old\n127.0.1.10 old\n", addr, "#127.0.1.1 old\n127.0.1.10 old\n" + addr + "\tnew\t# nuit-cloud\n"},
		// a line that merely mentions the marker in a longer comment is not ours
		{"10.0.0.8 other # nuit-cloud was here\n", addr, "10.0.0.8 other # nuit-cloud was here\n" + addr + "\tnew\t# nuit-cloud\n"},
		{"10.0.0.8 other # nuit-cloud was here\n127.0.1.1 old\n", addr, "10.0.0.8 other # nuit-cloud was here\n" + addr + " new # nuit-cloud\n"},
		// the marker with different spacing is still ours
		{"10.0.0.8 old #   nuit-cloud  \n", "", "10.0.0.8 old #   nuit-cloud  \n127.0.1.1\tnew\n"},
		// the marker on a line without an address is not ours
		{"# nuit-cloud\n", addr, "# nuit-cloud\n" + addr + "\tnew\t# nuit-cloud\n"},
	}
	for _, c := range cases {
		if got := RewriteHosts(c.in, "new", c.address, "old"); got != c.want {
			t.Errorf("%q (%q): got %q want %q", c.in, c.address, got, c.want)
		}
	}
}

func TestOtherLinesAreByteForByteUnchanged(t *testing.T) {
	odd := "# header\t with tab\n127.0.0.1\tlocalhost   \n\n  10.1.1.1 a b c\n::1 localhost\n\t\n#127.0.1.1 old\n127.0.1.10 old\n10.0.0.8 other # nuit-cloud was here\n"
	for _, address := range []string{"", addr} {
		s, _, _, hosts := setup(t, "old", "old", odd+"127.0.1.1 old\n"+"fe00::0 ip6-localnet")
		set(t, s, "new", address)
		want := odd + "127.0.1.1 new\n" + "fe00::0 ip6-localnet"
		if address != "" {
			want = odd + addr + " new # nuit-cloud\n" + "fe00::0 ip6-localnet"
		}
		if got := read(t, hosts); got != want {
			t.Errorf("%q: %q", address, got)
		}
		entries, _ := os.ReadDir(filepath.Dir(hosts))
		for _, e := range entries {
			if strings.Contains(e.Name(), ".nuit-") {
				t.Errorf("temp file left behind: %s", e.Name())
			}
		}
	}
}

func TestMissingHostsFileIsCreated(t *testing.T) {
	s, _, _, hosts := setup(t, "old", "old", "<none>")
	set(t, s, "new", "")
	if read(t, hosts) != "127.0.1.1\tnew\n" {
		t.Errorf("%q", read(t, hosts))
	}
}

func TestWhenHostnamectlFailsTheFileAndTheRunningNameAreSetDirectly(t *testing.T) {
	s, f, hp, hosts := setup(t, "old", "old", base+"127.0.1.1\told\n")
	f.ctlFails = true
	res := set(t, s, "new", addr)
	if res.Method != "hostname-file" || res.Outcome != "changed" || res.Previous != "old" {
		t.Errorf("%+v", res)
	}
	if read(t, hp) != "new\n" || f.running != "new" || read(t, hosts) != base+addr+"\tnew # nuit-cloud\n" {
		t.Errorf("file %q running %q hosts %q", read(t, hp), f.running, read(t, hosts))
	}
}

func TestBothWaysFailingIsAFailureAndHostsIsNotTouched(t *testing.T) {
	s, f, _, hosts := setup(t, "old", "old", base+"127.0.1.1\told\n")
	f.ctlFails, f.hostFails = true, true
	if _, err := s.Set(context.Background(), "new", addr); err == nil {
		t.Fatal("expected an error")
	}
	if read(t, hosts) != base+"127.0.1.1\told\n" {
		t.Errorf("hosts changed: %q", read(t, hosts))
	}
}

func TestValidate(t *testing.T) {
	good := map[string]string{"box": "box", "  Box-1 ": "box-1", "a": "a", strings.Repeat("a", 63): strings.Repeat("a", 63), "NCS-Mini-7K2P": "ncs-mini-7k2p"}
	for in, want := range good {
		got, a, err := Validate(map[string]any{"hostname": in})
		if err != nil || got != want || a != "" {
			t.Errorf("%q: %q %q %v", in, got, a, err)
		}
	}
	for _, a := range []string{"10.10.10.170", "192.168.1.1", "1.2.3.4", "100.64.0.1", "223.255.255.255", "172.16.0.1"} {
		name, got, err := Validate(map[string]any{"hostname": "box", "address": a})
		if err != nil || got != a || name != "box" {
			t.Errorf("%s: %q %q %v", a, name, got, err)
		}
	}
	bad := []map[string]any{
		{}, nil,
		{"hostname": ""}, {"hostname": "   "}, {"hostname": "a.b"}, {"hostname": "box.example.com"}, {"hostname": "-box"},
		{"hostname": "box-"}, {"hostname": strings.Repeat("a", 64)}, {"hostname": "bo x"}, {"hostname": "box_1"},
		{"hostname": "box\n127.0.0.1 evil"}, {"hostname": "bøx"}, {"hostname": 5}, {"hostname": nil}, {"hostname": []any{"box"}},
		{"hostname": "box", "extra": "x"}, {"name": "box"}, {"address": "10.0.0.1"},
	}
	for _, a := range []any{
		"", " 10.0.0.1", "10.0.0.1 ", "10.0.0.1\n", "10.0.0", "10.0.0.1.1", "10.0.0.256", "010.0.0.1", "10.0.0.01", "a.b.c.d", "::1", "fe80::1",
		"10.0.0.1; rm -rf /", "10.0.0.1 evil", "0.0.0.0", "127.0.0.1", "127.255.255.255", "127.0.1.1", "169.254.0.1", "169.254.255.255",
		"224.0.0.1", "239.1.1.1", "240.0.0.1", "255.255.255.255", 5, nil, []any{"10.0.0.1"},
	} {
		bad = append(bad, map[string]any{"hostname": "box", "address": a})
	}
	for _, p := range bad {
		if n, a, err := Validate(p); err == nil {
			t.Errorf("%v was accepted as %q %q", p, n, a)
		}
	}
}

func TestSetRefusesInvalidInputEvenIfCalledDirectly(t *testing.T) {
	s, f, _, _ := setup(t, "old", "old", base)
	for _, n := range []string{"", "A", "a.b", "-a", "a-", strings.Repeat("a", 64), "a b"} {
		if _, err := s.Set(context.Background(), n, ""); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
	for _, a := range []string{"127.0.0.1", "0.0.0.0", "x", "224.0.0.1"} {
		if _, err := s.Set(context.Background(), "box", a); err == nil {
			t.Errorf("address %q accepted", a)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %v", f.calls)
	}
}
