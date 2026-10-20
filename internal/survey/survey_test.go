package survey

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseOSRelease(t *testing.T) {
	o := ParseOSRelease("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\nHOME_URL=\"https://x\"\n")
	if o.ID != "debian" || o.VersionID != "13" || o.PrettyName != "Debian GNU/Linux 13 (trixie)" {
		t.Errorf("%+v", o)
	}
}

func TestParseCPUInfoFindsHardwareVirtualisation(t *testing.T) {
	intel := "processor\t: 0\nmodel name\t: Intel(R) Core(TM) i5\nflags\t\t: fpu vme sse vmx smx\n\nprocessor\t: 1\nmodel name\t: Intel(R) Core(TM) i5\nflags\t\t: fpu vme sse vmx smx\n"
	c := ParseCPUInfo(intel)
	if c.Logical != 2 || c.Model != "Intel(R) Core(TM) i5" || !c.Virtualisation.Supported || c.Virtualisation.Flag != "vmx" {
		t.Errorf("%+v", c)
	}
	amd := ParseCPUInfo("processor : 0\nmodel name : AMD Ryzen\nflags : fpu svm nx\n")
	if amd.Virtualisation.Flag != "svm" || !amd.Virtualisation.Supported {
		t.Errorf("%+v", amd)
	}
	none := ParseCPUInfo("processor : 0\nmodel name : Old CPU\nflags : fpu sse\n")
	if none.Virtualisation.Supported || none.Virtualisation.Flag != "" {
		t.Errorf("a CPU without the flag must not claim support: %+v", none)
	}
}

func TestParseMemInfo(t *testing.T) {
	m := ParseMemInfo("MemTotal:       16384000 kB\nMemFree: 1 kB\nMemAvailable:   8192000 kB\nSwapTotal:       2048000 kB\n")
	if m.TotalMiB != 16000 || m.AvailableMiB != 8000 || m.SwapMiB != 2000 {
		t.Errorf("%+v", m)
	}
}

func TestRealMountsKeepsStorageAndDropsTheKernelsVirtualOnes(t *testing.T) {
	text := "sysfs /sys sysfs rw 0 0\nproc /proc proc rw 0 0\n/dev/sda2 / ext4 rw 0 0\n/dev/sda1 /boot/efi vfat rw 0 0\ntmpfs /run tmpfs rw 0 0\noverlay /var/lib/docker/overlay2/x/merged overlay rw 0 0\n/dev/sdb1 /srv/with\\040space ext4 rw 0 0\n/dev/sda2 / ext4 rw 0 0\n/dev/loop0 /snap/core/1 squashfs ro 0 0\n"
	got := RealMounts(text)
	if len(got) != 3 || got[0].Mount != "/" || got[1].Mount != "/boot/efi" || got[2].Mount != "/srv/with space" {
		t.Errorf("%+v", got)
	}
}

func TestParseDefaultRoutePicksTheLowestMetricAndReadsTheGateway(t *testing.T) {
	// the kernel prints the gateway little-endian: 0101A8C0 is 192.168.1.1
	text := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"enp3s0\t00000000\t010AA8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"enp3s0\t0000A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	r := ParseDefaultRoute(text)
	if r == nil || r.Interface != "enp3s0" || r.Gateway != "192.168.10.1" {
		t.Errorf("%+v", r)
	}
	if ParseDefaultRoute("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n") != nil {
		t.Error("no default route must be nil")
	}
}

func TestParseSSHDConfigTakesTheFirstValueAsSshdDoes(t *testing.T) {
	c := ParseSSHDConfig("# a comment\nPermitRootLogin prohibit-password\nPermitRootLogin yes\nPasswordAuthentication no\n")
	if c.PermitRootLogin != "prohibit-password" || c.PasswordAuthentication != "no" {
		t.Errorf("%+v", c)
	}
	if (ParseSSHDConfig("")) != (SSH{}) {
		t.Error("nothing set must stay empty")
	}
}

// Collect against a made-up machine: a wireless-only box with no hardware virtualisation exposed.
func TestCollectReportsAWirelessMachineAsWireless(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		p := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("/etc/os-release", "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13\"\n")
	write("/proc/cpuinfo", "processor : 0\nmodel name : Test CPU\nflags : fpu vmx\n")
	write("/proc/meminfo", "MemTotal: 8388608 kB\nMemAvailable: 4194304 kB\n")
	write("/proc/mounts", "/dev/sda2 / ext4 rw 0 0\n/dev/sda2 /etc/hosts ext4 rw 0 0\n")
	write("/etc/hosts", "127.0.0.1 localhost\n")
	_ = os.MkdirAll(filepath.Join(root, "/"), 0o755)
	write("/proc/sys/kernel/osrelease", "6.12.0\n")
	write("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\nwlp2s0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n")
	write("/sys/class/net/wlp2s0/wireless/placeholder", "")
	write("/sys/class/net/wlp2s0/speed", "-1\n")
	write("/sys/fs/cgroup/cgroup.controllers", "cpuset cpu io memory pids\n")
	write("/etc/ssh/sshd_config", "PermitRootLogin no\n")

	s := Sources{
		Root: root,
		Run: func(_ context.Context, name string, _ ...string) (string, error) {
			if name == "timedatectl" {
				return "yes\n", nil
			}
			if name == "sshd" { // the effective settings: a file in sshd_config.d turned passwords off
				return "port 22\npermitrootlogin without-password\npasswordauthentication no\n", nil
			}
			return "", errors.New("unexpected command " + name)
		},
		Now:      func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) },
		Hostname: func() (string, error) { return "newbox", nil },
		Interfaces: func() ([]net.Interface, error) {
			return []net.Interface{
				{Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
				{Name: "wlp2s0", Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{0xaa, 0xbb, 0xcc, 0, 0, 1}},
			}, nil
		},
		Addrs: func(i net.Interface) ([]net.Addr, error) {
			if i.Name == "wlp2s0" {
				return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.1.50"), Mask: net.CIDRMask(24, 32)}}, nil
			}
			return nil, nil
		},
		Statfs: func(string) (int64, int64, error) { return 100 << 30, 40 << 30, nil },
		Open:   func(string) error { return errors.New("denied") },
	}
	r := Collect(context.Background(), s)

	if r.Hostname != "newbox" || r.OS.ID != "debian" || r.OS.Kernel != "6.12.0" || r.CollectedAt != "2026-10-20T12:00:00Z" {
		t.Errorf("basics: %+v", r)
	}
	if !r.CPU.Virtualisation.Supported || r.CPU.Virtualisation.KVMDevice {
		t.Errorf("the flag is there but there is no /dev/kvm in the fixture: %+v", r.CPU.Virtualisation)
	}
	if r.Memory.TotalMiB != 8192 || !r.Cgroups.V2 || len(r.Cgroups.Controllers) != 5 {
		t.Errorf("memory/cgroups: %+v %+v", r.Memory, r.Cgroups)
	}
	if len(r.Disks) != 1 || r.Disks[0].Mount != "/" || r.Disks[0].TotalMiB != 102400 || r.Disks[0].FreeMiB != 40960 {
		t.Errorf("disks: %+v", r.Disks)
	}
	var wlan *Interface
	for i := range r.Network.Interfaces {
		if r.Network.Interfaces[i].Name == "wlp2s0" {
			wlan = &r.Network.Interfaces[i]
		}
	}
	if wlan == nil || wlan.Kind != "wireless" || !wlan.Up || wlan.SpeedMbps != -1 || len(wlan.Addresses) != 1 || wlan.Addresses[0] != "192.168.1.50/24" {
		t.Errorf("wlan: %+v", wlan)
	}
	if r.Network.DefaultRoute == nil || r.Network.DefaultRoute.Interface != "wlp2s0" || r.Network.DefaultRoute.Gateway != "192.168.1.1" {
		t.Errorf("route: %+v", r.Network.DefaultRoute)
	}
	if r.Time.Synchronized == nil || !*r.Time.Synchronized || r.SSH.PermitRootLogin != "without-password" || r.SSH.PasswordAuthentication != "no" || r.SSH.Source != "effective" {
		t.Errorf("time/ssh: %+v %+v", r.Time, r.SSH)
	}
}

func TestCollectSaysWhatItCouldNotRead(t *testing.T) {
	s := Sources{
		Root:       t.TempDir(), // nothing in it
		Run:        func(context.Context, string, ...string) (string, error) { return "", errors.New("no timedatectl") },
		Now:        time.Now,
		Hostname:   func() (string, error) { return "", errors.New("no hostname") },
		Interfaces: func() ([]net.Interface, error) { return nil, errors.New("no interfaces") },
		Addrs:      func(net.Interface) ([]net.Addr, error) { return nil, nil },
		Statfs:     func(string) (int64, int64, error) { return 0, 0, nil },
		Open:       func(string) error { return nil },
	}
	r := Collect(context.Background(), s)
	if len(r.Problems) < 4 {
		t.Errorf("a machine the survey cannot read must say so, got %v", r.Problems)
	}
	if r.Time.Synchronized != nil {
		t.Error("an unknown clock state must stay unknown, not false")
	}
	if r.Disks == nil || r.Network.Interfaces == nil {
		t.Error("lists must be empty lists, not null, in the JSON")
	}
}

func TestArm64HasNoCPUFlagSoAUsableKVMDeviceIsTheEvidence(t *testing.T) {
	cpu := ParseCPUInfo("processor : 0\nBogoMIPS : 48.00\nFeatures : fp asimd\nCPU implementer : 0x61\nCPU part : 0x000\n")
	if cpu.Virtualisation.Supported || cpu.Model != "ARM CPU (implementer 0x61, part 0x000)" {
		t.Fatalf("%+v", cpu)
	}
	v := Virtualisation{KVMDevice: true, KVMReadWrite: true}
	AdjustVirtualisationForArch("arm64", &v)
	if !v.Supported || v.Flag != "kvm" {
		t.Errorf("a usable /dev/kvm on arm64 was not accepted: %+v", v)
	}
	for _, c := range []struct {
		arch string
		v    Virtualisation
	}{{"amd64", Virtualisation{KVMDevice: true, KVMReadWrite: true}}, {"arm64", Virtualisation{KVMDevice: true}}, {"arm64", Virtualisation{}}} {
		v := c.v
		AdjustVirtualisationForArch(c.arch, &v)
		if v.Supported {
			t.Errorf("%s %+v was accepted without the evidence", c.arch, c.v)
		}
	}
}

func TestTheInterruptControllerIsReadFromProcInterrupts(t *testing.T) {
	cases := map[string]string{"  3:  100  GICv3  30 Level  arch_timer\n": "GICv3", "  3:  100  GICv2  30 Level  arch_timer\n": "GICv2", "  3:  100  GIC-400  30 Level\n": "GICv2", "  0:  5  IO-APIC  2-edge  timer\n": ""}
	for text, want := range cases {
		if got := ParseInterruptController(text); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func sshSources(t *testing.T, config string, run func(string) (string, error)) Sources {
	t.Helper()
	root := t.TempDir()
	if config != "" {
		_ = os.MkdirAll(filepath.Join(root, "etc/ssh"), 0o755)
		if err := os.WriteFile(filepath.Join(root, "etc/ssh/sshd_config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Sources{Root: root, Run: func(_ context.Context, name string, _ ...string) (string, error) { return run(name) }}
}

func TestTheSSHSettingsAreWhatSshdWillUseNotJustWhatTheFileSays(t *testing.T) {
	// the file says nothing; the defaults and an included file decide
	s := sshSources(t, "# Include /etc/ssh/sshd_config.d/*.conf\n", func(name string) (string, error) {
		return "permitrootlogin no\npasswordauthentication no\npubkeyauthentication yes\n", nil
	})
	got := collectSSH(context.Background(), s, func(string, error) {})
	if got != (SSH{PermitRootLogin: "no", PasswordAuthentication: "no", Source: "effective"}) {
		t.Errorf("%+v", got)
	}
}

func TestWithoutSshdTheFileIsReadAndTheGapIsSaid(t *testing.T) {
	var problems []string
	s := sshSources(t, "PermitRootLogin yes\n", func(string) (string, error) { return "", errors.New("not found") })
	got := collectSSH(context.Background(), s, func(what string, _ error) { problems = append(problems, what) })
	if got.PermitRootLogin != "yes" || got.Source != "file" || len(problems) != 1 || problems[0] != "sshd -T" {
		t.Errorf("%+v %v", got, problems)
	}
	// neither sshd nor a file: nothing is claimed
	none := collectSSH(context.Background(), sshSources(t, "", func(string) (string, error) { return "", errors.New("no") }), func(string, error) {})
	if none != (SSH{}) {
		t.Errorf("%+v", none)
	}
}

func TestSshdTriedByItsAbsolutePathWhenNotOnThePath(t *testing.T) {
	s := sshSources(t, "", func(name string) (string, error) {
		if name == "/usr/sbin/sshd" {
			return "permitrootlogin prohibit-password\npasswordauthentication yes\n", nil
		}
		return "", errors.New("not on PATH")
	})
	if got := collectSSH(context.Background(), s, func(string, error) {}); got.Source != "effective" || got.PasswordAuthentication != "yes" {
		t.Errorf("%+v", got)
	}
}
