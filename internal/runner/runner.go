// Package runner is the agent's catalog: the only things it will ever do. Each is a named task from the platform; anything
// else is refused before it gets here. (The platform has its own copy of this list: adding a task means a release of both.)
package runner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mgjoyce/nuit-cloud-agent/internal/client"
	"github.com/mgjoyce/nuit-cloud-agent/internal/lanscan"
	"github.com/mgjoyce/nuit-cloud-agent/internal/profiles"
	"github.com/mgjoyce/nuit-cloud-agent/internal/runtimeinstall"
	"github.com/mgjoyce/nuit-cloud-agent/internal/sshconfirm"
	"github.com/mgjoyce/nuit-cloud-agent/internal/survey"
	"github.com/mgjoyce/nuit-cloud-agent/internal/taskverify"
	"github.com/mgjoyce/nuit-cloud-agent/internal/update"
)

// ProfileRunner checks or applies a profile (profiles.Runner; injectable so tests touch no real system).
type ProfileRunner interface {
	Run(ctx context.Context, profile string, apply bool, env ...string) (profiles.Result, int, error)
}

// SelfUpdater replaces the running agent with a newer build (update.Updater; injectable so tests touch no real binary).
type SelfUpdater interface {
	Apply(ctx context.Context, p update.Params) (update.Outcome, error)
}

// Runner performs verified tasks.
type Runner struct {
	// Version is the agent's own version, reported by ping.
	Version string
	// Survey reads the machine; injectable so tests need no real machine.
	Survey   func(ctx context.Context) survey.Report
	Profiles ProfileRunner
	// PlatformURL is the platform's address, handed to the network feature as the thing it must still reach from the new address.
	PlatformURL string
	// Runtime stages the Nuit Container Service runtime's files for a profile-apply that carries them; injectable for tests.
	Runtime RuntimeStager
	Updater SelfUpdater
	// SSHConfirm completes the SSH hardening: re-checks what sshd uses and cancels the rollback timer; injectable for tests.
	SSHConfirm func(ctx context.Context) (sshconfirm.Result, error)
	// Scan asks which addresses of a private range answer on the machine's network; injectable for tests.
	Scan func(ctx context.Context, first, last string) (lanscan.Result, error)
	// Smoke runs the runtime's smoke test in a namespace and returns what it printed and its exit status; injectable for tests.
	Smoke func(ctx context.Context, namespace string) (string, int, error)
	// Restart is set when a task has changed the agent itself (an update), so the loop restarts it after reporting.
	Restart *atomic.Bool
	Now     func() time.Time
}

// RestartRequested reports whether the agent must restart to finish what a task did.
func (r Runner) RestartRequested() bool { return r.Restart != nil && r.Restart.Load() }

// Names is the catalog: what this agent can do.
func (r Runner) Names() []string {
	return []string{"ping", "survey", "profile-check", "profile-apply", "update-agent", "runtime-smoke", "address-scan", "ssh-confirm"}
}

// Run performs one task, bounded by its timeout, and says how it went. It never panics the agent: a task that blows up is a
// failed task.
func (r Runner) Run(ctx context.Context, task *taskverify.Task) (report client.Report) {
	timeout := time.Duration(task.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			one := 1
			report = client.Report{Status: "failed", ExitCode: &one, Output: fmt.Sprintf("the task panicked: %v", p)}
		}
	}()

	zero, one := 0, 1
	switch task.Name {
	case "ping":
		return client.Report{Status: "done", ExitCode: &zero, Output: "pong", Result: map[string]any{"pong": true, "agentVersion": r.Version, "at": r.now().UTC().Format(time.RFC3339)}}
	case "survey":
		return client.Report{Status: "done", ExitCode: &zero, Output: "surveyed", Result: r.Survey(ctx)}
	case "profile-check", "profile-apply":
		return r.profile(ctx, task)
	case "update-agent":
		return r.selfUpdate(ctx, task)
	case "runtime-smoke":
		return r.smoke(ctx, task)
	case "address-scan":
		return r.scan(ctx, task)
	case "ssh-confirm":
		return r.sshConfirm(ctx)
	}
	return client.Report{Status: "failed", ExitCode: &one, Output: "unknown task " + task.Name}
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// profile checks or applies a profile. A check that finds drift has still done its job (the drift is the answer), so it is
// `done`. An apply is `failed` only if a feature actually failed; a feature that needs a person (a cable, SSH settings, the
// BIOS) is not a failure of the task, so the apply is `done` and the summary and the per-feature results say what is left.
func (r Runner) profile(ctx context.Context, task *taskverify.Task) client.Report {
	one := 1
	name, _ := task.Params["profile"].(string)
	if r.Profiles == nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: "this agent was built without profiles"}
	}
	apply := task.Name == "profile-apply"
	var env []string
	if raw, ok := task.Params["network"].(map[string]any); ok {
		addr, _ := raw["managementAddress"].(string)
		if !ipv4.MatchString(addr) || net.ParseIP(addr) == nil {
			return client.Report{Status: "failed", ExitCode: &one, Output: "the task carries no usable management address"}
		}
		env = append(env, "NCS_MANAGEMENT_ADDRESS="+addr, "NCS_PLATFORM_URL="+r.PlatformURL)
	}
	if raw, ok := task.Params["ssh"].(map[string]any); ok {
		fingerprints, err := sshFingerprints(raw)
		if err != nil {
			return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
		}
		env = append(env, "NCS_SSH_REQUIRE_FINGERPRINTS="+strings.Join(fingerprints, " "))
	}
	if raw, ok := task.Params["vlan"].(map[string]any); ok {
		vlanEnv, err := vlanEnvironment(raw, r.PlatformURL)
		if err != nil {
			return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
		}
		env = append(env, vlanEnv...)
	}
	if raw, ok := task.Params["runtime"].(map[string]any); ok && apply {
		if r.Runtime == nil {
			return client.Report{Status: "failed", ExitCode: &one, Output: "this agent cannot install the runtime"}
		}
		params, err := runtimeinstall.ParseParams(raw)
		if err != nil {
			return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
		}
		staged, cleanup, err := r.Runtime.Stage(ctx, params)
		if err != nil {
			return client.Report{Status: "failed", ExitCode: &one, Output: "could not stage the runtime: " + err.Error()}
		}
		defer cleanup()
		env = append(env, staged...)
	} else if ok {
		// a check needs no files: it only asks whether what is installed is healthy
		env = append(env, "NCS_RUNTIME_BUNDLE_SHA256="+stringParam(raw, "bundleSha256"))
	}
	res, exit, err := r.Profiles.Run(ctx, name, apply, env...)
	if err != nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
	}
	summary := summarise(res)
	for _, f := range res.Features {
		if apply && f.Status == "failed" {
			return client.Report{Status: "failed", ExitCode: &exit, Output: summary, Result: res}
		}
	}
	if apply && exit != 0 && len(res.Features) == 0 {
		return client.Report{Status: "failed", ExitCode: &exit, Output: summary + " (it produced no result)", Result: res}
	}
	return client.Report{Status: "done", ExitCode: &exit, Output: summary, Result: res}
}

func stringParam(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// RuntimeStager downloads and checks the runtime's files (runtimeinstall.Stager).
type RuntimeStager interface {
	Stage(ctx context.Context, p runtimeinstall.Params) (env []string, cleanup func(), err error)
}

// summarise says in a line or two how a profile run went.
func summarise(res profiles.Result) string {
	var satisfied int
	var need []string
	for _, f := range res.Features {
		if f.Status == "ok" || f.Status == "applied" {
			satisfied++
		} else {
			need = append(need, f.Name+" ("+f.Status+")")
		}
	}
	text := fmt.Sprintf("%s %s: %d of %d features satisfied", res.Mode, res.Profile, satisfied, len(res.Features))
	if len(need) > 0 {
		text += "; still to do: " + joinNames(need)
	}
	if res.RebootNeeded {
		text += "; a reboot is needed"
	}
	return text
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// selfUpdate installs the build the (signed) task names. When it replaces the binary it asks to be restarted, which the loop
// does after the result has been reported, so the platform hears of it before the agent goes away.
func (r Runner) selfUpdate(ctx context.Context, task *taskverify.Task) client.Report {
	one, zero := 1, 0
	if r.Updater == nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: "this agent cannot update itself"}
	}
	params, err := update.ParseParams(task.Params)
	if err != nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
	}
	outcome, err := r.Updater.Apply(ctx, params)
	if err != nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
	}
	if !outcome.Changed {
		return client.Report{Status: "done", ExitCode: &zero, Output: fmt.Sprintf("already at %s", outcome.To), Result: outcome}
	}
	if r.Restart != nil {
		r.Restart.Store(true)
	}
	return client.Report{Status: "done", ExitCode: &zero, Output: fmt.Sprintf("updated from %s to %s; restarting to start the new version", outcome.From, outcome.To), Result: outcome}
}

var ipv4 = regexp.MustCompile(`^([0-9]{1,3}\.){3}[0-9]{1,3}$`)

var smokeNamespace = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// SmokePath is where the runtime feature keeps the bundle's smoke test (copied from the verified bundle when it is installed).
const SmokePath = "/usr/local/lib/nuit-cloud/oci-saas-smoke.sh"

// RunSmoke is the real smoke test: the script the runtime's install left on the machine, run as root.
func RunSmoke(ctx context.Context, namespace string) (string, int, error) {
	if _, err := os.Stat(SmokePath); err != nil {
		return "", 0, fmt.Errorf("the runtime's smoke test is not on this machine (%s): apply the nuit-container-service profile first", SmokePath)
	}
	out, err := exec.CommandContext(ctx, "bash", SmokePath, namespace).CombinedOutput()
	exit := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exit, err = ee.ExitCode(), nil
	}
	return string(out), exit, err
}

// smoke runs the runtime's smoke test. It passes only if the script says so (SMOKE OK) and exits 0.
func (r Runner) smoke(ctx context.Context, task *taskverify.Task) client.Report {
	one := 1
	ns, _ := task.Params["namespace"].(string)
	if !smokeNamespace.MatchString(ns) {
		return client.Report{Status: "failed", ExitCode: &one, Output: "the smoke test needs a namespace name"}
	}
	run := r.Smoke
	if run == nil {
		run = RunSmoke
	}
	out, exit, err := run(ctx, ns)
	if err != nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
	}
	passed := exit == 0 && strings.Contains(out, "SMOKE OK")
	res := map[string]any{"namespace": ns, "passed": passed}
	status := "done"
	if !passed {
		status = "failed"
	}
	return client.Report{Status: status, ExitCode: &exit, Output: profiles.Truncate(out, 32*1024), Result: res}
}

// scan asks which addresses of the range answer. It changes nothing; an answer is a fact about the network, not a judgement.
func (r Runner) scan(ctx context.Context, task *taskverify.Task) client.Report {
	one, zero := 1, 0
	first, _ := task.Params["first"].(string)
	last, _ := task.Params["last"].(string)
	run := r.Scan
	if run == nil {
		run = lanscan.Real().Scan
	}
	res, err := run(ctx, first, last)
	if err != nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
	}
	return client.Report{Status: "done", ExitCode: &zero, Output: fmt.Sprintf("scanned %d addresses from %s (%s): %d answered", res.Scanned, res.From, res.Method, len(res.Answered)), Result: res}
}

// vlanEnvironment turns the signed VLAN slice into what the profile scripts read, after checking every part is what it should be.
func vlanEnvironment(raw map[string]any, platformURL string) ([]string, error) {
	number := func(k string, lo, hi float64) (string, error) {
		f, ok := raw[k].(float64)
		if !ok || f != float64(int(f)) || f < lo || f > hi {
			return "", fmt.Errorf("the VLAN %s is not a whole number from %v to %v", k, lo, hi)
		}
		return fmt.Sprintf("%d", int(f)), nil
	}
	address := func(v any) (string, error) {
		s, _ := v.(string)
		if !ipv4.MatchString(s) || net.ParseIP(s) == nil {
			return "", fmt.Errorf("%q is not an IPv4 address", s)
		}
		return s, nil
	}
	id, err := number("id", 1, 4094)
	if err != nil {
		return nil, err
	}
	prefix, err := number("prefix", 16, 30)
	if err != nil {
		return nil, err
	}
	addr, err := address(raw["address"])
	if err != nil {
		return nil, fmt.Errorf("the VLAN address: %w", err)
	}
	gateway, err := address(raw["gateway"])
	if err != nil {
		return nil, fmt.Errorf("the VLAN gateway: %w", err)
	}
	list, _ := raw["tenantAddresses"].([]any)
	if len(list) < 1 || len(list) > 32 {
		return nil, errors.New("the VLAN carries 1 to 32 tenant addresses")
	}
	var tenants []string
	for _, v := range list {
		a, err := address(v)
		if err != nil {
			return nil, fmt.Errorf("a tenant address: %w", err)
		}
		tenants = append(tenants, a)
	}
	return []string{"NCS_VLAN_ID=" + id, "NCS_VLAN_PREFIX=" + prefix, "NCS_VLAN_ADDRESS=" + addr, "NCS_VLAN_GATEWAY=" + gateway, "NCS_VLAN_TENANT_ADDRESSES=" + strings.Join(tenants, " "), "NCS_PLATFORM_URL=" + platformURL}, nil
}

var sshFingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// sshFingerprints reads the keys the platform says must still be able to log in, refusing anything that is not a fingerprint.
func sshFingerprints(raw map[string]any) ([]string, error) {
	list, _ := raw["requireFingerprints"].([]any)
	if len(list) < 1 || len(list) > 10 {
		return nil, errors.New("the SSH task names 1 to 10 keys that must still be able to log in")
	}
	var out []string
	for _, v := range list {
		f, _ := v.(string)
		if !sshFingerprint.MatchString(f) {
			return nil, fmt.Errorf("%q is not a key fingerprint", f)
		}
		out = append(out, f)
	}
	return out, nil
}

// sshConfirm completes the SSH hardening once the platform has tested sshd from outside.
func (r Runner) sshConfirm(ctx context.Context) client.Report {
	one, zero := 1, 0
	run := r.SSHConfirm
	if run == nil {
		run = sshconfirm.Confirmer{Run: func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		}}.Confirm
	}
	res, err := run(ctx)
	if err != nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: err.Error(), Result: res}
	}
	return client.Report{Status: "done", ExitCode: &zero, Output: res.Note, Result: res}
}
