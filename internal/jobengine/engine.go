// Package jobengine queues and runs backend operations as trackable jobs.
package jobengine

import (
	"context"
	"sync"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/google/uuid"
)

// State describes the lifecycle stage of a render job.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// maxRetainedTerminalJobs caps how many TERMINAL jobs (completed/failed/
// cancelled) are retained. A long-lived stdio server would otherwise
// accumulate finished jobs forever. Queued/running jobs are never counted
// against or evicted by this cap.
const maxRetainedTerminalJobs = 100

// isTerminal reports whether a state is a final state.
func isTerminal(s State) bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

// Job holds the observable state of a single render job.
type Job struct {
	ID          string
	State       State
	Progress    backend.Progress
	OutputPaths []string
	Err         string
	cancel      context.CancelFunc
}

// Engine queues and runs backend operations with a bounded concurrency limit.
type Engine struct {
	be   backend.Backend
	sem  chan struct{}
	mu   sync.Mutex
	jobs map[string]*Job
	// terminalOrder holds terminal job IDs in the order they became terminal
	// (oldest first), so the oldest can be evicted when over maxTerminal.
	terminalOrder []string
	// maxTerminal caps retained terminal jobs. Defaults to
	// maxRetainedTerminalJobs; overridable in white-box tests.
	maxTerminal int
}

// New creates an Engine with the given backend and max concurrency.
func New(be backend.Backend, concurrency int) *Engine {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Engine{
		be:          be,
		sem:         make(chan struct{}, concurrency),
		jobs:        make(map[string]*Job),
		maxTerminal: maxRetainedTerminalJobs,
	}
}

// StartStabilize enqueues a stabilize job and returns its ID immediately.
// onProgress, if non-nil, is called for each progress update emitted by the
// backend (e.g. to emit MCP progress notifications). Pass nil to suppress.
func (e *Engine) StartStabilize(req backend.StabilizeRequest, onProgress backend.ProgressFunc) string {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{ID: uuid.NewString(), State: StateQueued, cancel: cancel}
	e.mu.Lock()
	e.jobs[j.ID] = j
	e.mu.Unlock()

	go func() {
		// #1: release context on every terminal path (cancel is idempotent -
		// safe even if Engine.Cancel already called it).
		defer cancel()

		// #2: honor Cancel() while the job is still queued (waiting for the slot).
		select {
		case e.sem <- struct{}{}:
			defer func() { <-e.sem }()
		case <-ctx.Done():
			e.finishJob(j.ID, func(j *Job) { j.State = StateCancelled })
			return
		}

		e.set(j.ID, func(j *Job) { j.State = StateRunning })
		res, err := e.be.Stabilize(ctx, req, func(p backend.Progress) {
			e.set(j.ID, func(j *Job) { j.Progress = p })
			if onProgress != nil {
				onProgress(p)
			}
		})
		e.finishJob(j.ID, func(j *Job) {
			switch {
			case ctx.Err() != nil:
				j.State = StateCancelled
			case err != nil:
				j.State = StateFailed
				j.Err = err.Error()
			default:
				j.State = StateCompleted
				if res != nil {
					j.OutputPaths = res.OutputPaths
				}
			}
		})
	}()
	return j.ID
}

// Backend returns the backend used by this engine.
func (e *Engine) Backend() backend.Backend { return e.be }

// Cancel signals cancellation for the given job. Returns false if not found.
func (e *Engine) Cancel(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return false
	}
	j.cancel()
	return true
}

// Status returns a snapshot of the job's current state.
func (e *Engine) Status(id string) (Job, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns snapshots of all known jobs.
func (e *Engine) List() []Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Job, 0, len(e.jobs))
	for _, j := range e.jobs {
		out = append(out, *j)
	}
	return out
}

// set applies a mutation to a job under the engine lock.
func (e *Engine) set(id string, f func(*Job)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if j, ok := e.jobs[id]; ok {
		f(j)
	}
}

// finishJob applies a (terminal) mutation under the engine lock. When the job
// transitions from non-terminal to terminal it is recorded for retention, and
// the oldest terminal jobs beyond maxTerminal are evicted. Queued/running jobs
// are never recorded, so eviction can never touch them.
func (e *Engine) finishJob(id string, f func(*Job)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return
	}
	prev := j.State
	f(j)
	if !isTerminal(j.State) || isTerminal(prev) {
		return // not a non-terminal→terminal transition; record exactly once
	}
	e.terminalOrder = append(e.terminalOrder, id)
	for len(e.terminalOrder) > e.maxTerminal {
		oldest := e.terminalOrder[0]
		e.terminalOrder = e.terminalOrder[1:]
		// terminalOrder only ever holds IDs recorded at a terminal transition,
		// so this never deletes a queued/running job.
		delete(e.jobs, oldest)
	}
}
