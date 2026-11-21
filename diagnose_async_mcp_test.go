package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingRun is a diagnoseFunc that runs until release is closed or its
// context ends.
func blockingRun(release <-chan struct{}) diagnoseFunc {
	return func(ctx context.Context, _, _ string, _ int, progress progressFunc) (string, error) {
		progress(1, "turn")
		select {
		case <-release:
			return "answer", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func waitForStatus(t *testing.T, r *diagnoseRunner, id, want string) diagnoseJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := r.get(id); ok && job.Status == want {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, _ := r.get(id)
	t.Fatalf("job %s status = %q, want %q", id, job.Status, want)
	return job
}

func TestDiagnoseRunnerConcurrentStartsRespectLimit(t *testing.T) {
	release := make(chan struct{})
	r := newDiagnoseRunner(3, blockingRun(release))

	var started atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.start("q", "", 10); err == nil {
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := started.Load(); got != 3 {
		t.Errorf("started %d jobs, want 3", got)
	}
	if err := r.acquire(); err == nil {
		t.Error("synchronous acquire succeeded while async jobs hold every slot")
	}

	close(release)
	r.wg.Wait()
	if err := r.acquire(); err != nil {
		t.Errorf("acquire after the jobs finished: %v", err)
	}
	r.release()
}

func TestDiagnoseRunnerSyncCallsShareTheLimit(t *testing.T) {
	r := newDiagnoseRunner(2, blockingRun(make(chan struct{})))
	for range 2 {
		if err := r.acquire(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.start("q", "", 10); err == nil {
		t.Error("async start succeeded while synchronous calls hold every slot")
	}
	r.release()
	r.release()
}

func TestDiagnoseRunnerJobLifecycle(t *testing.T) {
	release := make(chan struct{})
	r := newDiagnoseRunner(3, blockingRun(release))
	now := time.Unix(1_800_000_000, 0)
	var clock sync.Mutex
	r.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return now }
	advance := func(d time.Duration) { clock.Lock(); now = now.Add(d); clock.Unlock() }

	job, err := r.start("q", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "running" {
		t.Errorf("status = %q, want running", job.Status)
	}
	close(release)
	done := waitForStatus(t, r, job.ID, "done")
	if done.Answer != "answer" || done.Iteration != 1 {
		t.Errorf("job = %+v, want answer after one turn", done)
	}

	advance(finishedJobTTL - time.Second)
	if _, ok := r.get(job.ID); !ok {
		t.Fatal("job purged before its TTL")
	}
	advance(2 * time.Second)
	if _, ok := r.get(job.ID); ok {
		t.Error("job still present after its TTL")
	}
}

func TestDiagnoseRunnerJobErrorsAndPanics(t *testing.T) {
	r := newDiagnoseRunner(3, func(context.Context, string, string, int, progressFunc) (string, error) {
		return "", errors.New("bedrock down")
	})
	job, err := r.start("q", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForStatus(t, r, job.ID, "error"); got.Err != "bedrock down" {
		t.Errorf("err = %q, want bedrock down", got.Err)
	}

	r = newDiagnoseRunner(1, func(context.Context, string, string, int, progressFunc) (string, error) {
		panic("boom")
	})
	job, err = r.start("q", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, r, job.ID, "error")
	if err := r.acquire(); err != nil {
		t.Errorf("a panicked job kept its slot: %v", err)
	}
}

func TestDiagnoseRunnerShutdown(t *testing.T) {
	release := make(chan struct{})
	r := newDiagnoseRunner(3, blockingRun(release))
	job, err := r.start("q", "", 10)
	if err != nil {
		t.Fatal(err)
	}

	// A job that finishes within the wait completes normally.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.shutdown(ctx)
	if got, _ := r.get(job.ID); got.Status != "done" {
		t.Errorf("status = %q, want done", got.Status)
	}
	if _, err := r.start("q", "", 10); err == nil {
		t.Error("start succeeded after shutdown")
	}

	// A job still running when the wait ends is cancelled.
	r = newDiagnoseRunner(3, blockingRun(make(chan struct{})))
	job, err = r.start("q", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r.shutdown(ctx)
	if got, _ := r.get(job.ID); got.Status != "error" {
		t.Errorf("status = %q, want error after cancellation", got.Status)
	}
}
