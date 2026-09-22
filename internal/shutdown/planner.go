package shutdown

import (
	"sync"
	"sync/atomic"
	"time"
)

// Planner decides when to power off the machine. It is fed VS Code agent
// hook events (SessionStart / Stop) and fires onTrigger exactly once when
// all of the following hold:
//
//   - armed: the user asked for "power off at the end of the next task"
//   - a Stop event arrived
//   - no SessionStart arrived since that Stop (checked when the grace
//     timer expires)
//
// A SessionStart inside the grace window cancels only the pending
// shutdown of the current stop — the armed flag is KEPT, so the next
// stop (without a following start) still triggers the power-off.
//
// The planner is safe for concurrent use; HookEvent is called from the
// mirror server's HTTP handler goroutines.
type Planner struct {
	mu        sync.Mutex
	grace     time.Duration
	armed     bool
	lastStart time.Time
	lastStop  time.Time
	timer     *time.Timer
	triggered atomic.Bool
	onTrigger func()
}

// NewPlanner creates a planner with the given grace window. onTrigger
// runs (on the timer's goroutine) exactly once, when the power-off
// conditions are met.
func NewPlanner(grace time.Duration, onTrigger func()) *Planner {
	return &Planner{grace: grace, onTrigger: onTrigger}
}

// Arm enables the power-off at the next qualifying stop.
func (p *Planner) Arm() {
	p.mu.Lock()
	p.armed = true
	p.mu.Unlock()
}

// Disarm disables the power-off and cancels any pending trigger.
func (p *Planner) Disarm() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed = false
	p.stopTimer()
}

// Armed reports whether a power-off has been requested.
func (p *Planner) Armed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.armed
}

// HookEvent feeds one hook event name into the state machine. Only
// "SessionStart" and "Stop" are meaningful; everything else is ignored
// (in particular SubagentStop: a subagent finishing is not the end of
// the user's task).
func (p *Planner) HookEvent(name string) {
	now := time.Now()
	p.mu.Lock()
	switch name {
	case "SessionStart":
		p.lastStart = now
		// A new task started: cancel the pending shutdown of the
		// previous stop. Armed stays — the next stop still counts.
		p.stopTimer()
	case "Stop":
		p.lastStop = now
		if p.armed && !p.triggered.Load() {
			p.armTimer()
		}
	}
	p.mu.Unlock()
}

// armTimer (re)starts the grace timer. Called with p.mu held.
func (p *Planner) armTimer() {
	p.stopTimer()
	p.timer = time.AfterFunc(p.grace, p.fire)
}

// stopTimer cancels the pending timer, if any. Called with p.mu held.
func (p *Planner) stopTimer() {
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}

// fire runs on the timer goroutine when the grace window elapses. It
// re-checks the conditions under the lock (a start may have arrived in
// the meantime) and invokes onTrigger at most once.
func (p *Planner) fire() {
	p.mu.Lock()
	if !p.armed || p.triggered.Load() || !p.lastStop.After(p.lastStart) {
		p.stopTimer()
		p.mu.Unlock()
		return
	}
	p.triggered.Store(true)
	p.timer = nil
	fn := p.onTrigger
	p.mu.Unlock()
	if fn != nil {
		fn()
	}
}
