package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mgjoyce/nuit-cloud-agent/internal/profiles"
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

type fakeProfiles struct {
	res   profiles.Result
	exit  int
	err   error
	calls []string
}

func (f *fakeProfiles) Run(_ context.Context, profile string, apply bool) (profiles.Result, int, error) {
	mode := "check"
	if apply {
		mode = "apply"
	}
	f.calls = append(f.calls, mode+" "+profile)
	r := f.res
	r.Profile, r.Mode = profile, mode
	return r, f.exit, f.err
}

func withProfiles(f *fakeProfiles) Runner {
	r := newRunner()
	r.Profiles = f
	return r
}

func profileTask(name string) *taskverify.Task {
	return &taskverify.Task{Name: name, TimeoutSeconds: 30, Params: map[string]any{"profile": "nuit-cloud-server"}}
}

func TestACheckThatFindsDriftIsDoneBecauseTheDriftIsTheAnswer(t *testing.T) {
	f := &fakeProfiles{exit: 1, res: profiles.Result{Features: []profiles.Feature{{Name: "user", Status: "ok"}, {Name: "ncs_ssh", Status: "drift"}}}}
	r := withProfiles(f).Run(context.Background(), profileTask("profile-check"))
	if r.Status != "done" || *r.ExitCode != 1 || r.Output != "check nuit-cloud-server: 1 of 2 features satisfied; still to do: ncs_ssh (drift)" {
		t.Errorf("%+v", r)
	}
	if len(f.calls) != 1 || f.calls[0] != "check nuit-cloud-server" {
		t.Errorf("a check must not apply: %v", f.calls)
	}
	if r.Result.(profiles.Result).Features[1].Status != "drift" {
		t.Error("the per-feature findings must be sent")
	}
}

func TestAnApplyIsDoneOnlyWhenEveryFeatureIsSatisfied(t *testing.T) {
	good := &fakeProfiles{exit: 0, res: profiles.Result{Features: []profiles.Feature{{Name: "user", Status: "ok"}, {Name: "journal", Status: "applied"}}}}
	r := withProfiles(good).Run(context.Background(), profileTask("profile-apply"))
	if r.Status != "done" || *r.ExitCode != 0 || good.calls[0] != "apply nuit-cloud-server" {
		t.Errorf("%+v %v", r, good.calls)
	}

	needsPerson := &fakeProfiles{exit: 1, res: profiles.Result{RebootNeeded: true, Features: []profiles.Feature{{Name: "kvm", Status: "manual"}, {Name: "journal", Status: "applied"}}}}
	r = withProfiles(needsPerson).Run(context.Background(), profileTask("profile-apply"))
	if r.Status != "failed" || *r.ExitCode != 1 || r.Output != "apply nuit-cloud-server: 1 of 2 features satisfied; still to do: kvm (manual); a reboot is needed" {
		t.Errorf("%+v", r)
	}
	if r.Result == nil {
		t.Error("the findings must still be sent when an apply is not finished")
	}
}

func TestAProfileThatCannotRunFailsWithTheReason(t *testing.T) {
	for name, err := range map[string]error{"root": profiles.ErrNeedsRoot, "unknown": errors.New(`this agent does not carry a profile named "x"`)} {
		r := withProfiles(&fakeProfiles{err: err}).Run(context.Background(), profileTask("profile-apply"))
		if r.Status != "failed" || r.Output != err.Error() || r.Result != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
	rn := newRunner() // built without profiles
	if r := rn.Run(context.Background(), profileTask("profile-check")); r.Status != "failed" {
		t.Errorf("%+v", r)
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
