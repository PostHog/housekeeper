package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Async diagnose: clickhouse_diagnose_async starts an investigation and
// returns a job id immediately; clickhouse_diagnose_result polls it. This
// sidesteps client tool timeouts entirely — the investigation runs server-side
// regardless of whether the client waits — and stops wasting Bedrock work when
// a client disconnects mid-run.
//
// The job store is in-memory and therefore single-instance: with multiple
// server replicas, a poll can land on an instance that doesn't hold the job.
// Adequate for single-replica deployments; add sticky routing or an external
// store before scaling out.

// finishedJobTTL is how long completed/failed jobs stay pollable.
const finishedJobTTL = 30 * time.Minute

// diagnoseGrace is added to a job's budget for its context deadline, covering
// the final summarize turn(s) after the budget itself is exhausted.
const diagnoseGrace = 90 * time.Second

type diagnoseAsyncArgs struct {
	Question string `json:"question"`
	Cluster  string `json:"cluster,omitempty"`
	// BudgetSeconds follows the same semantics as the synchronous tool but
	// with a higher default (async callers aren't racing a client timeout).
	// Clamped to bedrock.max_seconds.
	BudgetSeconds int `json:"budget_seconds,omitempty"`
}

type diagnoseResultArgs struct {
	JobID string `json:"job_id"`
}

type diagnoseJob struct {
	ID        string
	Question  string
	Status    string // "running", "done", "error"
	Answer    string
	Err       string
	Started   time.Time
	Finished  time.Time
	Iteration int32
	Budget    int
}

// diagnoseFunc runs one investigation; runDiagnosis in production.
type diagnoseFunc func(ctx context.Context, question, cluster string, budgetSeconds int, progress progressFunc) (string, error)

// diagnoseRunner holds the async job store and the one concurrency limit
// shared by synchronous and async diagnoses. The limit is checked and the slot
// reserved under the same lock that inserts a job, so concurrent starts cannot
// overshoot it.
type diagnoseRunner struct {
	mu      sync.Mutex
	jobs    map[string]*diagnoseJob
	active  int // synchronous calls plus running async jobs
	max     int
	closing bool // shutdown has begun; no new async jobs

	now func() time.Time
	run diagnoseFunc

	// Async jobs run on baseCtx, which shutdown cancels once its wait is over.
	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func newDiagnoseRunner(maxConcurrent int, run diagnoseFunc) *diagnoseRunner {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &diagnoseRunner{
		jobs: map[string]*diagnoseJob{}, max: maxConcurrent,
		now: time.Now, run: run,
		baseCtx: ctx, cancel: cancel,
	}
}

// errBusyLocked is the error for a reached concurrency limit. Callers hold r.mu.
func (r *diagnoseRunner) errBusyLocked() error {
	return fmt.Errorf("%d investigations already running (max %d) — poll existing jobs or retry shortly", r.active, r.max)
}

// acquire reserves a slot for a synchronous diagnosis; release frees it.
func (r *diagnoseRunner) acquire() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active >= r.max {
		return r.errBusyLocked()
	}
	r.active++
	return nil
}

func (r *diagnoseRunner) release() {
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
}

// purgeLocked drops finished jobs past their TTL. Callers hold r.mu.
func (r *diagnoseRunner) purgeLocked() {
	for id, j := range r.jobs {
		if j.Status != "running" && r.now().Sub(j.Finished) > finishedJobTTL {
			delete(r.jobs, id)
		}
	}
}

// start reserves a slot, records a running job and runs it in the background.
func (r *diagnoseRunner) start(question, cluster string, budget int) (diagnoseJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeLocked()
	if r.closing {
		return diagnoseJob{}, fmt.Errorf("server is shutting down; retry shortly")
	}
	if r.active >= r.max {
		return diagnoseJob{}, r.errBusyLocked()
	}
	id, err := newJobID()
	if err != nil {
		return diagnoseJob{}, fmt.Errorf("generating job id: %w", err)
	}
	job := &diagnoseJob{ID: id, Question: question, Status: "running", Started: r.now(), Budget: budget}
	r.jobs[id] = job
	r.active++
	r.wg.Add(1)
	go r.runJob(job, question, cluster, budget)
	return *job, nil
}

// get returns a snapshot of a job.
func (r *diagnoseRunner) get(id string) (diagnoseJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeLocked()
	job, ok := r.jobs[id]
	if !ok {
		return diagnoseJob{}, false
	}
	return *job, true
}

// runJob executes one async investigation on the runner's background context
// (the originating request has already returned) and records the outcome. A
// panic is contained to the job.
func (r *diagnoseRunner) runJob(job *diagnoseJob, question, cluster string, budget int) {
	defer r.wg.Done()
	status, answer, errMsg := "error", "", ""
	defer func() {
		if rec := recover(); rec != nil {
			logrus.WithField("job_id", job.ID).Errorf("diagnose: async job panic: %v", rec)
			status, answer, errMsg = "error", "", fmt.Sprintf("internal error: %v", rec)
		}
		r.mu.Lock()
		job.Status, job.Answer, job.Err = status, answer, errMsg
		job.Finished = r.now()
		r.active--
		r.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.baseCtx, time.Duration(budget)*time.Second+diagnoseGrace)
	defer cancel()
	progress := func(iter int32, _ string) {
		r.mu.Lock()
		job.Iteration = iter
		r.mu.Unlock()
	}

	ans, err := r.run(ctx, question, cluster, budget, progress)
	if err != nil {
		logrus.WithError(err).WithField("job_id", job.ID).Warn("diagnose: async job failed")
		errMsg = err.Error()
		return
	}
	logrus.WithFields(logrus.Fields{
		"job_id": job.ID, "elapsed": r.now().Sub(job.Started).Seconds(),
	}).Info("diagnose: async job complete")
	status, answer = "done", ans
}

// shutdown stops new async jobs and waits for running ones until ctx is done.
// Jobs still running then are cancelled, and it waits briefly for them to
// record their outcome.
func (r *diagnoseRunner) shutdown(ctx context.Context) {
	r.mu.Lock()
	r.closing = true
	running := r.active
	r.mu.Unlock()
	if running > 0 {
		logrus.WithField("running", running).Info("diagnose: waiting for running investigations")
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	stopNew := context.AfterFunc(ctx, r.cancel)
	defer stopNew()
	select {
	case <-done:
		r.cancel()
		return
	case <-ctx.Done():
	}
	logrus.Warn("diagnose: shutdown wait ended; cancelling running investigations")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func newJobID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "dj_" + hex.EncodeToString(b), nil
}

// resolveAsyncBudgetSeconds picks the budget for an async job: the caller's
// value clamped to the cap, defaulting to bedrock.async_default_seconds
// (itself clamped) when unset.
func resolveAsyncBudgetSeconds(requested int) int {
	if requested <= 0 {
		requested = viper.GetInt("bedrock.async_default_seconds")
	}
	if c := capBudgetSeconds(); c > 0 && (requested <= 0 || requested > c) {
		return c
	}
	return requested
}

// registerDiagnoseAsyncTools adds the async start/poll tool pair. Registered
// alongside the synchronous tool whenever Bedrock is configured.
func registerDiagnoseAsyncTools(srv *mcp.Server, runner *diagnoseRunner) {
	startDesc := fmt.Sprintf(`Start a ClickHouse investigation in the background and return a job id immediately — use this instead of clickhouse_diagnose when the investigation should run longer than your client's tool timeout. Same in-account agent and elevated access as clickhouse_diagnose; poll clickhouse_diagnose_result for the answer (a model turn takes ~15-40s, so poll every ~30s).

budget_seconds: wall-clock budget (default %d, cap %d). Finished results stay pollable for %d minutes.`,
		resolveAsyncBudgetSeconds(0), capBudgetSeconds(), int(finishedJobTTL.Minutes()))

	mcp.AddTool[diagnoseAsyncArgs, map[string]any](
		srv,
		&mcp.Tool{
			Name:        "clickhouse_diagnose_async",
			Title:       "Start background ClickHouse diagnosis",
			Description: startDesc,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args diagnoseAsyncArgs) (*mcp.CallToolResult, map[string]any, error) {
			q := strings.TrimSpace(args.Question)
			if q == "" {
				return nil, nil, fmt.Errorf("question is required")
			}
			budget := resolveAsyncBudgetSeconds(args.BudgetSeconds)
			job, err := runner.start(q, args.Cluster, budget)
			if err != nil {
				return nil, nil, err
			}

			logrus.WithFields(logrus.Fields{"job_id": job.ID, "budget_seconds": budget}).Info("diagnose: async job started")
			summary := fmt.Sprintf("started job %s (budget %ds) — poll clickhouse_diagnose_result with this job_id in ~30s", job.ID, budget)
			data := map[string]any{"job_id": job.ID, "status": "running", "budget_seconds": budget}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: summary}},
			}, data, nil
		},
	)

	mcp.AddTool[diagnoseResultArgs, map[string]any](
		srv,
		&mcp.Tool{
			Name:        "clickhouse_diagnose_result",
			Title:       "Poll a background ClickHouse diagnosis",
			Description: "Fetch the status/result of a clickhouse_diagnose_async job. Returns status running (with elapsed seconds and the agent's turn counter), done (with the answer), or error. Poll every ~30s while running.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args diagnoseResultArgs) (*mcp.CallToolResult, map[string]any, error) {
			id := strings.TrimSpace(args.JobID)
			if id == "" {
				return nil, nil, fmt.Errorf("job_id is required")
			}
			snapshot, ok := runner.get(id)
			if !ok {
				return nil, nil, fmt.Errorf("unknown job %q (finished jobs expire after %d minutes; jobs do not survive a server restart)", id, int(finishedJobTTL.Minutes()))
			}

			data := map[string]any{
				"job_id": snapshot.ID, "status": snapshot.Status,
				"budget_seconds": snapshot.Budget,
			}
			var text string
			switch snapshot.Status {
			case "running":
				elapsed := int(runner.now().Sub(snapshot.Started).Seconds())
				data["elapsed_seconds"] = elapsed
				data["model_turns"] = snapshot.Iteration
				text = fmt.Sprintf("running: %ds elapsed of %ds budget, model turn %d — poll again in ~30s", elapsed, snapshot.Budget, snapshot.Iteration)
			case "done":
				data["answer"] = snapshot.Answer
				data["elapsed_seconds"] = int(snapshot.Finished.Sub(snapshot.Started).Seconds())
				text = snapshot.Answer
			default: // error
				data["error"] = snapshot.Err
				text = "investigation failed: " + snapshot.Err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: text}},
			}, data, nil
		},
	)
}
