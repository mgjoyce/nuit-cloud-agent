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
	running    string
	ctlFails   bool
	hostFails  bool
	calls      []string
	failsAfter bool
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

func TestAValidRenameUsesHostnamectlAndRewritesTheLoopbackLine(t *testing.T) {
	s, f, _, hosts := setup(t, "old", "old", base+"127.0.1.1\told\n10.0.0.5\tnas # keep\n")
	res, err := s.Set(context.Background(), "new")
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Previous: "old", Hostname: "new", Outcome: "changed", Method: "hostnamectl"}) {
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

func TestAlreadyThatHostnameChangesNothing(t *testing.T) {
	content := base + "127.0.1.1\tnew\n"
	s, f, _, hosts := setup(t, "new", "new", content)
	res, err := s.Set(context.Background(), "new")
	if err != nil || res.Outcome != "unchanged" || res.Method != "none" || res.Previous != "new" {
		t.Fatalf("%+v %v", res, err)
	}
	if read(t, hosts) != content || strings.Join(f.calls, "|") != "hostname" {
		t.Errorf("something changed: %v", f.calls)
	}
	// and a second change after a change is also unchanged
	s, _, _, _ = setup(t, "old", "old", base)
	if _, err := s.Set(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
}

func TestRunningNameRightButHostsStaleIsStillRepaired(t *testing.T) {
	s, _, _, hosts := setup(t, "new", "new", base+"127.0.1.1\told\n")
	res, err := s.Set(context.Background(), "new")
	if err != nil || res.Outcome != "changed" {
		t.Fatalf("%+v %v", res, err)
	}
	// the name that was replaced earlier is no longer known, so it stays as an alias (never dropped by guesswork)
	if read(t, hosts) != base+"127.0.1.1\tnew old\n" {
		t.Errorf("%q", read(t, hosts))
	}
}

func TestAliasesOtherThanTheOldNameAreKept(t *testing.T) {
	s, _, _, hosts := setup(t, "old", "old", base+"127.0.1.1 old old.example.com extra  # note\n")
	if _, err := s.Set(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, hosts); got != base+"127.0.1.1 new old.example.com extra # note\n" {
		t.Errorf("%q", got)
	}
}

func TestNoLoopbackLineAddsOne(t *testing.T) {
	s, _, _, hosts := setup(t, "old", "old", base)
	if _, err := s.Set(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, hosts); got != base+"127.0.1.1\tnew\n" {
		t.Errorf("%q", got)
	}
}

func TestOddLayouts(t *testing.T) {
	cases := map[string]string{
		// no trailing newline
		"127.0.0.1 localhost": "127.0.0.1 localhost\n127.0.1.1\tnew\n",
		// the line is last and has no newline, indented
		"127.0.0.1 localhost\n  127.0.1.1   old": "127.0.0.1 localhost\n  127.0.1.1   new",
		// a commented-out 127.0.1.1 is not a line; blank lines and the second 127.0.1.1 are left alone
		"#127.0.1.1 old\n\n127.0.1.1 old\n127.0.1.1 old\n": "#127.0.1.1 old\n\n127.0.1.1 new\n127.0.1.1 old\n",
		// 127.0.1.10 is a different address
		"127.0.1.10 old\n": "127.0.1.10 old\n127.0.1.1\tnew\n",
		"":                 "127.0.1.1\tnew\n",
	}
	for in, want := range cases {
		if got := RewriteHosts(in, "new", "old"); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestMissingHostsFileIsCreated(t *testing.T) {
	s, _, _, hosts := setup(t, "old", "old", "<none>")
	if _, err := s.Set(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if read(t, hosts) != "127.0.1.1\tnew\n" {
		t.Errorf("%q", read(t, hosts))
	}
}

func TestOtherLinesAreByteForByteUnchanged(t *testing.T) {
	odd := "# header\t with tab\n127.0.0.1\tlocalhost   \n\n  10.1.1.1 a b c\n::1 localhost\n\t\n"
	s, _, _, hosts := setup(t, "old", "old", odd+"127.0.1.1 old\n"+"fe00::0 ip6-localnet")
	if _, err := s.Set(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, hosts); got != odd+"127.0.1.1 new\n"+"fe00::0 ip6-localnet" {
		t.Errorf("%q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(hosts))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".nuit-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWhenHostnamectlFailsTheFileAndTheRunningNameAreSetDirectly(t *testing.T) {
	s, f, hp, hosts := setup(t, "old", "old", base+"127.0.1.1\told\n")
	f.ctlFails = true
	res, err := s.Set(context.Background(), "new")
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != "hostname-file" || res.Outcome != "changed" || res.Previous != "old" {
		t.Errorf("%+v", res)
	}
	if read(t, hp) != "new\n" || f.running != "new" || read(t, hosts) != base+"127.0.1.1\tnew\n" {
		t.Errorf("file %q running %q hosts %q", read(t, hp), f.running, read(t, hosts))
	}
}

func TestBothWaysFailingIsAFailureAndHostsIsNotTouched(t *testing.T) {
	s, f, _, hosts := setup(t, "old", "old", base+"127.0.1.1\told\n")
	f.ctlFails, f.hostFails = true, true
	if _, err := s.Set(context.Background(), "new"); err == nil {
		t.Fatal("expected an error")
	}
	if read(t, hosts) != base+"127.0.1.1\told\n" {
		t.Errorf("hosts changed: %q", read(t, hosts))
	}
}

func TestValidate(t *testing.T) {
	good := map[string]string{"box": "box", "  Box-1 ": "box-1", "a": "a", strings.Repeat("a", 63): strings.Repeat("a", 63), "NCS-Mini-7K2P": "ncs-mini-7k2p"}
	for in, want := range good {
		got, err := Validate(map[string]any{"hostname": in})
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	bad := []map[string]any{
		{},
		nil,
		{"hostname": ""},
		{"hostname": "   "},
		{"hostname": "a.b"},
		{"hostname": "box.example.com"},
		{"hostname": "-box"},
		{"hostname": "box-"},
		{"hostname": strings.Repeat("a", 64)},
		{"hostname": "bo x"},
		{"hostname": "box_1"},
		{"hostname": "box\n127.0.0.1 evil"},
		{"hostname": "bøx"},
		{"hostname": 5},
		{"hostname": nil},
		{"hostname": []any{"box"}},
		{"hostname": "box", "extra": "x"},
		{"name": "box"},
	}
	for _, p := range bad {
		if got, err := Validate(p); err == nil {
			t.Errorf("%v was accepted as %q", p, got)
		}
	}
}

func TestSetRefusesAnInvalidNameEvenIfCalledDirectly(t *testing.T) {
	s, f, _, _ := setup(t, "old", "old", base)
	for _, n := range []string{"", "A", "a.b", "-a", "a-", strings.Repeat("a", 64), "a b"} {
		if _, err := s.Set(context.Background(), n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %v", f.calls)
	}
}
