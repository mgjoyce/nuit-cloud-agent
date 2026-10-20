// Package runner is the agent's catalog: the only things it will ever do. Each is a named task from the platform; anything
// else is refused before it gets here. (The platform has its own copy of this list: adding a task means a release of both.)
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/mgjoyce/nuit-cloud-agent/internal/client"
	"github.com/mgjoyce/nuit-cloud-agent/internal/survey"
	"github.com/mgjoyce/nuit-cloud-agent/internal/taskverify"
)

// Runner performs verified tasks.
type Runner struct {
	// Version is the agent's own version, reported by ping.
	Version string
	// Survey reads the machine; injectable so tests need no real machine.
	Survey func(ctx context.Context) survey.Report
	Now    func() time.Time
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
		// Profiles arrive with the profile features (ticket #57). Until then this agent says so plainly instead of pretending.
		return client.Report{Status: "failed", ExitCode: &one, Output: fmt.Sprintf("%s is not supported by this version of the agent (%s)", task.Name, r.Version)}
	}
	return client.Report{Status: "failed", ExitCode: &one, Output: "unknown task " + task.Name}
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
