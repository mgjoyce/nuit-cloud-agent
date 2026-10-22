package fwconfirm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, armed bool, nftErr error) (Confirmer, *[]string, string) {
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
		if name == "nft" {
			return "", nftErr
		}
		return "", nil
	}}, &calls, file
}

func TestConfirmingCancelsTheTimerWhenTheTableIsLoaded(t *testing.T) {
	c, calls, file := setup(t, true, nil)
	r, err := c.Confirm(context.Background())
	if err != nil || !r.Confirmed || !r.Armed {
		t.Fatalf("%+v %v", r, err)
	}
	if got := strings.Join(*calls, "|"); !strings.Contains(got, "nft list table inet nuit_cloud") || !strings.Contains(got, "systemctl stop nuit-cloud-firewall-rollback.timer") {
		t.Errorf("calls: %s", got)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the armed marker was left behind")
	}
}

func TestWithoutTheTableTheTimerIsLeftToRun(t *testing.T) {
	c, calls, file := setup(t, true, errors.New("no such table"))
	r, err := c.Confirm(context.Background())
	if err == nil || r.Confirmed || !r.Armed {
		t.Fatalf("%+v %v", r, err)
	}
	if strings.Contains(strings.Join(*calls, "|"), "systemctl stop") {
		t.Error("the timer was stopped although the firewall is not loaded")
	}
	if _, err := os.Stat(file); err != nil {
		t.Error("the armed marker was removed")
	}
}

func TestNothingWaitingIsNotAnError(t *testing.T) {
	c, calls, _ := setup(t, false, nil)
	r, err := c.Confirm(context.Background())
	if err != nil || r.Armed || r.Confirmed || len(*calls) != 0 {
		t.Fatalf("%+v %v %v", r, err, *calls)
	}
}
