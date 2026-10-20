package runner

import (
	"context"
	"testing"
	"time"

	"github.com/mgjoyce/nuit-cloud-agent/internal/survey"
	"github.com/mgjoyce/nuit-cloud-agent/internal/taskverify"
)

func newRunner() Runner {
	return Runner{
		Version: "9.9.9",
		Now:     func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) },
		Survey:  func(context.Context) survey.Report { return survey.Report{Hostname: "box"} },
	}
}

func TestPingAnswers(t *testing.T) {
	r := newRunner().Run(context.Background(), &taskverify.Task{Name: "ping", TimeoutSeconds: 30})
	if r.Status != "done" || r.Output != "pong" || *r.ExitCode != 0 {
		t.Errorf("%+v", r)
	}
	res := r.Result.(map[string]any)
	if res["agentVersion"] != "9.9.9" || res["at"] != "2026-10-20T12:00:00Z" {
		t.Errorf("%+v", res)
	}
}

func TestSurveyReturnsTheReport(t *testing.T) {
	r := newRunner().Run(context.Background(), &taskverify.Task{Name: "survey", TimeoutSeconds: 120})
	if r.Status != "done" || r.Result.(survey.Report).Hostname != "box" {
		t.Errorf("%+v", r)
	}
}

func TestProfilesAreSaidToBeUnsupportedNotFaked(t *testing.T) {
	for _, name := range []string{"profile-check", "profile-apply"} {
		r := newRunner().Run(context.Background(), &taskverify.Task{Name: name, TimeoutSeconds: 30, Params: map[string]any{"profile": "nuit-cloud-server"}})
		if r.Status != "failed" || *r.ExitCode != 1 || r.Output == "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestAnUnknownTaskFailsAndNothingRuns(t *testing.T) {
	ran := false
	rn := newRunner()
	rn.Survey = func(context.Context) survey.Report { ran = true; return survey.Report{} }
	r := rn.Run(context.Background(), &taskverify.Task{Name: "rm-rf", TimeoutSeconds: 30})
	if r.Status != "failed" || ran {
		t.Errorf("%+v ran=%v", r, ran)
	}
}

func TestATaskThatPanicsIsAFailedTaskNotACrashedAgent(t *testing.T) {
	rn := newRunner()
	rn.Survey = func(context.Context) survey.Report { panic("boom") }
	r := rn.Run(context.Background(), &taskverify.Task{Name: "survey", TimeoutSeconds: 30})
	if r.Status != "failed" || r.Output != "the task panicked: boom" {
		t.Errorf("%+v", r)
	}
}

func TestTheTimeoutReachesTheTask(t *testing.T) {
	rn := newRunner()
	var deadline time.Time
	rn.Survey = func(ctx context.Context) survey.Report { deadline, _ = ctx.Deadline(); return survey.Report{} }
	start := time.Now()
	rn.Run(context.Background(), &taskverify.Task{Name: "survey", TimeoutSeconds: 120})
	if d := deadline.Sub(start); d < 119*time.Second || d > 121*time.Second {
		t.Errorf("deadline in %v, want ~120s", d)
	}
}
