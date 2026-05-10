package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"eino-cli/backend/runtime/eino"
)

// fakePlanRuntime implements eino.Runtime to drive (*Model).handlePlanCmd
// without touching the real deep-agent stack. Each SetPlanMode call records
// the requested plan flag so tests can assert call ordering / dedup.
type fakePlanRuntime struct {
	plan       bool
	calls      []bool
	failNext   bool // when true, next SetPlanMode returns an error and refuses to flip
	failReason string
}

func (f *fakePlanRuntime) Execute(ctx context.Context, prompt string) (eino.Result, error) {
	return eino.Result{}, nil
}
func (f *fakePlanRuntime) ExecuteStream(ctx context.Context, prompt string, onChunk eino.StreamChunkHandler) (eino.Result, error) {
	return eino.Result{}, nil
}
func (f *fakePlanRuntime) ClearHistory() {}
func (f *fakePlanRuntime) Name() string  { return "fake" }
func (f *fakePlanRuntime) SetPlanMode(ctx context.Context, plan bool) error {
	if f.failNext {
		f.failNext = false
		return errors.New(f.failReason)
	}
	f.calls = append(f.calls, plan)
	f.plan = plan
	return nil
}

func newPlanModel(rt eino.Runtime) *Model {
	return &Model{
		rt:       rt,
		messages: freshMessages(),
	}
}

func TestHandlePlanCmd_DefaultArgToggles(t *testing.T) {
	rt := &fakePlanRuntime{}
	m := newPlanModel(rt)

	m.handlePlanCmd("/plan")
	if !m.planMode || rt.plan != true {
		t.Errorf("/plan toggled OFF→ON expected; planMode=%v rt.plan=%v", m.planMode, rt.plan)
	}
	m.handlePlanCmd("/plan")
	if m.planMode || rt.plan != false {
		t.Errorf("/plan toggled ON→OFF expected; planMode=%v rt.plan=%v", m.planMode, rt.plan)
	}
}

func TestHandlePlanCmd_OnOffExplicit(t *testing.T) {
	rt := &fakePlanRuntime{}
	m := newPlanModel(rt)

	m.handlePlanCmd("/plan on")
	if !m.planMode {
		t.Error("/plan on must enable plan mode")
	}
	m.handlePlanCmd("/plan off")
	if m.planMode {
		t.Error("/plan off must disable plan mode")
	}
}

func TestHandlePlanCmd_NoOpDoesntCallRuntime(t *testing.T) {
	rt := &fakePlanRuntime{}
	m := newPlanModel(rt)

	m.handlePlanCmd("/plan off") // already off
	if len(rt.calls) != 0 {
		t.Errorf("no-op /plan off should not call runtime; calls=%v", rt.calls)
	}
	m.handlePlanCmd("/plan on")
	m.handlePlanCmd("/plan on") // already on
	if len(rt.calls) != 1 {
		t.Errorf("no-op /plan on should not call runtime again; calls=%v", rt.calls)
	}
}

func TestHandlePlanCmd_RuntimeFailureKeepsViewInSync(t *testing.T) {
	rt := &fakePlanRuntime{failNext: true, failReason: "rebuild boom"}
	m := newPlanModel(rt)

	m.handlePlanCmd("/plan on")
	if m.planMode {
		t.Error("planMode should NOT flip when runtime SetPlanMode fails")
	}
	// system message should surface the failure reason for the user.
	last := m.messages[len(m.messages)-1]
	if last.Role != "system" || !strings.Contains(last.Content, "rebuild boom") {
		t.Errorf("expected error system message; got %+v", last)
	}
}

func TestHandlePlanCmd_BadArg(t *testing.T) {
	rt := &fakePlanRuntime{}
	m := newPlanModel(rt)

	m.handlePlanCmd("/plan banana")
	if len(rt.calls) != 0 {
		t.Errorf("bad arg should not call runtime; calls=%v", rt.calls)
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != "system" || !strings.Contains(last.Content, "usage:") {
		t.Errorf("expected usage system message; got %+v", last)
	}
}
