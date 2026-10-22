// Package fwconfirm completes the baseline firewall the `nuit-cloud-firewall` profile starts. The profile loads the rules and leaves a
// timer armed that would delete them again. The platform then tests from outside (SSH reachable, the ports it asked to open not
// blocked, a port nobody opened dropped) and, only if all of that holds, sends the `firewall-confirm` task, which is this: it checks
// once more that the table is really loaded, and then cancels the timer. If the table is not there it leaves the timer to do its work.
package fwconfirm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ArmedFile exists while the rollback timer is waiting for a confirmation (written by the ncs_firewall feature).
const ArmedFile = "/var/lib/nuit-cloud-agent/address/firewall/armed"

// Timer is the systemd timer that removes the firewall.
const Timer = "nuit-cloud-firewall-rollback.timer"

// Table is the nftables table the feature loads.
const Table = "nuit_cloud"

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

// Confirm cancels the rollback if the firewall's table is loaded.
func (c Confirmer) Confirm(ctx context.Context) (Result, error) {
	armed := filepath.Join(c.Root, ArmedFile)
	if _, err := os.Stat(armed); err != nil {
		return Result{Note: "no firewall was waiting for a confirmation"}, nil
	}
	if _, err := c.Run(ctx, "nft", "list", "table", "inet", Table); err != nil {
		return Result{Armed: true}, errors.New("the firewall's table is not loaded, so the timer was left to put things back: " + err.Error())
	}
	if _, err := c.Run(ctx, "systemctl", "stop", Timer); err != nil {
		return Result{Armed: true}, errors.New("could not stop the rollback timer: " + err.Error())
	}
	_ = os.Remove(armed)
	return Result{Armed: true, Confirmed: true, Note: "the firewall is loaded and the rollback is cancelled"}, nil
}
