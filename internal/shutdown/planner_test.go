package shutdown

import (
	"sync/atomic"
	"testing"
	"time"
)

// newTestPlanner builds a planner with a short grace window that counts
// trigger fires.
func newTestPlanner(t *testing.T, fires *atomic.Int32) *Planner {
	t.Helper()
	return NewPlanner(50*time.Millisecond, func() { fires.Add(1) })
}

// waitFires polls until the fire count reaches want (or the deadline).
func waitFires(t *testing.T, fires *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for fires.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d fires, got %d", want, fires.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitNoFire lets the grace window (plus margin) elapse and asserts no
// NEW trigger fired since the baseline count.
func waitNoFire(t *testing.T, fires *atomic.Int32, baseline int32) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	if n := fires.Load(); n != baseline {
		t.Fatalf("expected no new trigger (baseline %d), got %d", baseline, n)
	}
}

func TestPlannerArmStopFires(t *testing.T) {
	var fires atomic.Int32
	p := newTestPlanner(t, &fires)
	p.Arm()
	p.HookEvent("Stop")
	waitFires(t, &fires, 1)
}

func TestPlannerNotArmedDoesNotFire(t *testing.T) {
	var fires atomic.Int32
	p := newTestPlanner(t, &fires)
	p.HookEvent("Stop")
	waitNoFire(t, &fires, 0)
}

func TestPlannerStartInsideGraceCancelsButKeepsArmed(t *testing.T) {
	var fires atomic.Int32
	p := newTestPlanner(t, &fires)
	p.Arm()
	p.HookEvent("Stop")
	// A new task starts inside the grace window: the pending shutdown
	// is cancelled, but the armed flag is kept.
	p.HookEvent("SessionStart")
	waitNoFire(t, &fires, 0)
	if !p.Armed() {
		t.Fatal("start must not disarm the planner")
	}
	// The next stop (no following start) still triggers.
	p.HookEvent("Stop")
	waitFires(t, &fires, 1)
}

func TestPlannerDisarmCancels(t *testing.T) {
	var fires atomic.Int32
	p := newTestPlanner(t, &fires)
	p.Arm()
	p.HookEvent("Stop")
	p.Disarm()
	waitNoFire(t, &fires, 0)
	if p.Armed() {
		t.Fatal("disarm must clear the armed flag")
	}
}

func TestPlannerRepeatedStopsFireOnce(t *testing.T) {
	var fires atomic.Int32
	p := newTestPlanner(t, &fires)
	p.Arm()
	p.HookEvent("Stop")
	p.HookEvent("Stop")
	waitFires(t, &fires, 1)
	// Give a second (impossible) trigger a chance to happen.
	waitNoFire(t, &fires, 1)
}

func TestPlannerIgnoresOtherEvents(t *testing.T) {
	var fires atomic.Int32
	p := newTestPlanner(t, &fires)
	p.Arm()
	p.HookEvent("SubagentStop")
	p.HookEvent("PreToolUse")
	waitNoFire(t, &fires, 0)
}
