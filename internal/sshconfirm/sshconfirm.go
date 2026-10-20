// Package sshconfirm completes the SSH hardening the `nuit-cloud-ssh` profile starts. The profile turns passwords and root login
// off and leaves a timer armed that would turn them back on. The platform then tests sshd from outside and, only if sshd answers
// and offers a key and no password, sends the `ssh-confirm` task, which is this: it checks once more that sshd really uses the
// hardened settings, and then cancels the timer. If the settings are not what they should be it leaves the timer to do its work.
package sshconfirm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ArmedFile exists while the rollback timer is waiting for a confirmation (written by the ncs_ssh_harden feature).
const ArmedFile = "/var/lib/nuit-cloud-agent/address/ssh/armed"

// Timer is the systemd timer that restores password logins.
const Timer = "nuit-cloud-ssh-rollback.timer"

type Result struct {
	// Armed says whether a rollback was waiting; false means there was nothing to confirm.
	Armed     bool   `json:"armed"`
	Confirmed bool   `json:"confirmed"`
	Note      string `json:"note,omitempty"`
}

type Confirmer struct {
	// Root prefixes the armed file's path (tests); empty on a real machine.
	Root string
	// Run runs a command and returns what it printed.
	Run func(ctx context.Context, name string, args ...string) (string, error)
}

// effective reads the two settings out of `sshd -T`.
func (c Confirmer) effective(ctx context.Context) (root, password string, err error) {
	var out string
	for _, binary := range []string{"sshd", "/usr/sbin/sshd"} {
		if out, err = c.Run(ctx, binary, "-T"); err == nil {
			break
		}
	}
	if err != nil {
		return "", "", errors.New("could not ask sshd what it uses: " + err.Error())
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch strings.ToLower(f[0]) {
		case "permitrootlogin":
			root = strings.ToLower(f[1])
		case "passwordauthentication":
			password = strings.ToLower(f[1])
		}
	}
	return root, password, nil
}

// Confirm cancels the rollback if sshd uses the hardened settings.
func (c Confirmer) Confirm(ctx context.Context) (Result, error) {
	armed := filepath.Join(c.Root, ArmedFile)
	if _, err := os.Stat(armed); err != nil {
		return Result{Note: "no SSH hardening was waiting for a confirmation"}, nil
	}
	root, password, err := c.effective(ctx)
	if err != nil {
		return Result{Armed: true}, err
	}
	if root == "yes" || password != "no" {
		return Result{Armed: true}, errors.New("sshd does not use the hardened settings (PermitRootLogin " + root + ", PasswordAuthentication " + password + "), so the timer was left to put things back")
	}
	if _, err := c.Run(ctx, "systemctl", "stop", Timer); err != nil {
		return Result{Armed: true}, errors.New("could not stop the rollback timer: " + err.Error())
	}
	_ = os.Remove(armed)
	return Result{Armed: true, Confirmed: true, Note: "SSH is hardened and the rollback is cancelled"}, nil
}
