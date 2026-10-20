// Package runner is the agent's catalog: the only things it will ever do. Each is a named task from the platform; anything
// else is refused before it gets here. (The platform has its own copy of this list: adding a task means a release of both.)
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/mgjoyce/nuit-cloud-agent/internal/client"
	"github.com/mgjoyce/nuit-cloud-agent/internal/profiles"
	"github.com/mgjoyce/nuit-cloud-agent/internal/survey"
	"github.com/mgjoyce/nuit-cloud-agent/internal/taskverify"
)

// ProfileRunner checks or applies a profile (profiles.Runner; injectable so tests touch no real system).
type ProfileRunner interface {
	Run(ctx context.Context, profile string, apply bool) (profiles.Result, int, error)
}

// Runner performs verified tasks.
type Runner struct {
	// Version is the agent's own version, reported by ping.
	Version string
	// Survey reads the machine; injectable so tests need no real machine.
	Survey   func(ctx context.Context) survey.Report
	Profiles ProfileRunner
	Now      func() time.Time
}

// Names is the catalog: what this agent can do.
func (r Runner) Names() []string {
	return []string{"ping", "survey", "profile-check", "profile-apply"}
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
// `done`; an apply is `done` only when every feature ends up satisfied, otherwise `failed` with what is left, because a
// machine that still needs a person is not finished.
func (r Runner) profile(ctx context.Context, task *taskverify.Task) client.Report {
	one := 1
	name, _ := task.Params["profile"].(string)
	if r.Profiles == nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: "this agent was built without profiles"}
	}
	apply := task.Name == "profile-apply"
	res, exit, err := r.Profiles.Run(ctx, name, apply)
	if err != nil {
		return client.Report{Status: "failed", ExitCode: &one, Output: err.Error()}
	}
	summary := summarise(res)
	if apply && exit != 0 {
		return client.Report{Status: "failed", ExitCode: &exit, Output: summary, Result: res}
	}
	return client.Report{Status: "done", ExitCode: &exit, Output: summary, Result: res}
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
