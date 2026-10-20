package sshconfirm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, armed bool, sshdOut string, sshdErr error) (Confirmer, *[]string, string) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, ArmedFile)
	if armed {
		_ = os.MkdirAll(filepath.Dir(file), 0o755)
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	return Confirmer{Root: root, Run: func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "sshd" || name == "/usr/sbin/sshd" {
			return sshdOut, sshdErr
		}
		return "", nil
	}}, &calls, file
}

func TestConfirmingCancelsTheTimerOnlyWhenSshdUsesTheHardenedSettings(t *testing.T) {
	c, calls, file := setup(t, true, "permitrootlogin no\npasswordauthentication no\n", nil)
	r, err := c.Confirm(context.Background())
	if err != nil || !r.Confirmed || !r.Armed {
		t.Fatalf("%+v %v", r, err)
	}
	if got := strings.Join(*calls, "|"); !strings.Contains(got, "systemctl stop nuit-cloud-ssh-rollback.timer") {
		t.Errorf("timer not stopped: %s", got)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the armed marker was left behind")
	}
}

func TestAnyKeyOnlyRootValueIsAccepted(t *testing.T) {
	for _, root := range []string{"no", "without-password", "prohibit-password", "forced-commands-only"} {
		c, _, _ := setup(t, true, "permitrootlogin "+root+"\npasswordauthentication no\n", nil)
		if r, err := c.Confirm(context.Background()); err != nil || !r.Confirmed {
			t.Errorf("%s: %+v %v", root, r, err)
		}
	}
}

func TestIfSshdStillAllowsPasswordsOrRootTheTimerIsLeftToRun(t *testing.T) {
	for _, out := range []string{"permitrootlogin no\npasswordauthentication yes\n", "permitrootlogin yes\npasswordauthentication no\n", "port 22\n"} {
		c, calls, file := setup(t, true, out, nil)
		r, err := c.Confirm(context.Background())
		if err == nil || r.Confirmed {
			t.Errorf("%q was confirmed: %+v", out, r)
		}
		for _, call := range *calls {
			if strings.HasPrefix(call, "systemctl") {
				t.Errorf("the timer was touched: %s", call)
			}
		}
		if _, e := os.Stat(file); e != nil {
			t.Error("the armed marker was removed although nothing was confirmed")
		}
	}
	c, _, _ := setup(t, true, "", errors.New("no sshd"))
	if _, err := c.Confirm(context.Background()); err == nil {
		t.Error("an unreadable sshd was confirmed")
	}
}

func TestWithNothingArmedThereIsNothingToConfirm(t *testing.T) {
	c, calls, _ := setup(t, false, "", nil)
	r, err := c.Confirm(context.Background())
	if err != nil || r.Armed || r.Confirmed || len(*calls) != 0 {
		t.Errorf("%+v %v %v", r, err, *calls)
	}
}
