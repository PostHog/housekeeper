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

// jobRunGrace is added to a job's budget for its context deadline, covering
// the final summarize turn(s) after the budget itself is exhausted.
const jobRunGrace = 90 * time.Second

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

type diagnoseJobStore struct {
	mu   sync.Mutex
	jobs map[string]*diagnoseJob
}

func newDiagnoseJobStore() *diagnoseJobStore {
	return &diagnoseJobStore{jobs: map[string]*diagnoseJob{}}
}

// purgeLocked drops finished jobs past their TTL. Callers hold s.mu.
func (s *diagnoseJobStore) purgeLocked() {
	for id, j := range s.jobs {
		if j.Status != "running" && time.Since(j.Finished) > finishedJobTTL {
			delete(s.jobs, id)
		}
	}
}

func (s *diagnoseJobStore) runningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.Status == "running" {
			n++
		}
	}
	return n
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
func registerDiagnoseAsyncTools(srv *mcp.Server) {
	store := newDiagnoseJobStore()
	maxConcurrent := viper.GetInt("bedrock.max_concurrent_jobs")

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
		func(ctx context.Context, ss *mcp.ServerSession, req *mcp.CallToolParamsFor[diagnoseAsyncArgs]) (*mcp.CallToolResultFor[map[string]any], error) {
			q := strings.TrimSpace(req.Arguments.Question)
			if q == "" {
				return nil, fmt.Errorf("question is required")
			}
			if n := store.runningCount(); n >= maxConcurrent {
				return nil, fmt.Errorf("%d investigations already running (max %d) — poll existing jobs or retry shortly", n, maxConcurrent)
			}

			id, err := newJobID()
			if err != nil {
				return nil, fmt.Errorf("generating job id: %w", err)
			}
			budget := resolveAsyncBudgetSeconds(req.Arguments.BudgetSeconds)
			job := &diagnoseJob{
				ID: id, Question: q, Status: "running",
				Started: time.Now(), Budget: budget,
			}
			store.mu.Lock()
			store.purgeLocked()
			store.jobs[id] = job
			store.mu.Unlock()

			go runDiagnoseJob(store, job, q, req.Arguments.Cluster, budget)

			logrus.WithFields(logrus.Fields{"job_id": id, "budget_seconds": budget}).Info("diagnose: async job started")
			summary := fmt.Sprintf("started job %s (budget %ds) — poll clickhouse_diagnose_result with this job_id in ~30s", id, budget)
			return &mcp.CallToolResultFor[map[string]any]{
				Content: []mcp.Content{&mcp.TextContent{Text: summary}},
				StructuredContent: map[string]any{
					"job_id": id, "status": "running", "budget_seconds": budget,
				},
			}, nil
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
		func(ctx context.Context, ss *mcp.ServerSession, req *mcp.CallToolParamsFor[diagnoseResultArgs]) (*mcp.CallToolResultFor[map[string]any], error) {
			id := strings.TrimSpace(req.Arguments.JobID)
			if id == "" {
				return nil, fmt.Errorf("job_id is required")
			}
			store.mu.Lock()
			store.purgeLocked()
			job, ok := store.jobs[id]
			var snapshot diagnoseJob
			if ok {
				snapshot = *job
			}
			store.mu.Unlock()
			if !ok {
				return nil, fmt.Errorf("unknown job %q (finished jobs expire after %d minutes)", id, int(finishedJobTTL.Minutes()))
			}

			data := map[string]any{
				"job_id": snapshot.ID, "status": snapshot.Status,
				"budget_seconds": snapshot.Budget,
			}
			var text string
			switch snapshot.Status {
			case "running":
				elapsed := int(time.Since(snapshot.Started).Seconds())
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
			return &mcp.CallToolResultFor[map[string]any]{
				Content:           []mcp.Content{&mcp.TextContent{Text: text}},
				StructuredContent: data,
			}, nil
		},
	)
}

// runDiagnoseJob executes one async investigation. It runs on its own
// background context (the originating request has already returned) and
// records the outcome on the job. A panic is contained to the job.
func runDiagnoseJob(store *diagnoseJobStore, job *diagnoseJob, question, cluster string, budget int) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(budget)*time.Second+jobRunGrace)
	defer cancel()

	finish := func(status, answer, errMsg string) {
		store.mu.Lock()
		job.Status, job.Answer, job.Err = status, answer, errMsg
		job.Finished = time.Now()
		store.mu.Unlock()
	}
	defer func() {
		if r := recover(); r != nil {
			logrus.WithField("job_id", job.ID).Errorf("diagnose: async job panic: %v", r)
			finish("error", "", fmt.Sprintf("internal error: %v", r))
		}
	}()

	progress := func(iter int32, _ string) {
		store.mu.Lock()
		job.Iteration = iter
		store.mu.Unlock()
	}

	answer, err := runDiagnosis(ctx, question, cluster, budget, progress)
	if err != nil {
		logrus.WithError(err).WithField("job_id", job.ID).Warn("diagnose: async job failed")
		finish("error", "", err.Error())
		return
	}
	logrus.WithFields(logrus.Fields{
		"job_id": job.ID, "elapsed": time.Since(job.Started).Seconds(),
	}).Info("diagnose: async job complete")
	finish("done", answer, "")
}
