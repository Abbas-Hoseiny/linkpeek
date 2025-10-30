package scanner

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	svc, err := NewService(Config{
		DataDir:    dir,
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
		Logger:     log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}
	t.Cleanup(svc.Shutdown)
	return svc, dir
}

func TestServiceCreateListDelete(t *testing.T) {
	svc, _ := newTestService(t)

	job, err := svc.CreateJob(CreateJobRequest{
		Name:            "Test Job",
		Method:          http.MethodGet,
		URL:             "https://example.com",
		IntervalSeconds: 60,
		Active:          false,
	})
	if err != nil {
		t.Fatalf("CreateJob returned error: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected job ID to be populated")
	}

	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	if err := svc.DeleteJob(job.ID); err != nil {
		t.Fatalf("DeleteJob failed: %v", err)
	}
	if err := svc.DeleteJob("missing"); err == nil {
		t.Fatal("expected DeleteJob to report missing job")
	}

	if jobs := svc.ListJobs(); len(jobs) != 0 {
		t.Fatalf("expected no jobs after deletion, got %d", len(jobs))
	}
}

func TestServiceActiveJobProducesResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	}))
	defer server.Close()

	svc, _ := newTestService(t)
	// Override client so the service can reach the test server.
	svc.httpClient = server.Client()

	job, err := svc.CreateJob(CreateJobRequest{
		Name:            "Active",
		Method:          http.MethodGet,
		URL:             server.URL,
		IntervalSeconds: 15,
		Active:          true,
	})
	if err != nil {
		t.Fatalf("CreateJob returned error: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		results := svc.Results(5, job.ID)
		if len(results) > 0 {
			if results[0].Status != http.StatusOK {
				t.Fatalf("expected status 200, got %d", results[0].Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for scanner result")
		}
		time.Sleep(50 * time.Millisecond)
	}

	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].LastRun.IsZero() {
		t.Fatal("expected LastRun to be updated")
	}

	if err := svc.ClearResults(); err != nil {
		t.Fatalf("ClearResults returned error: %v", err)
	}
	if results := svc.Results(5, job.ID); len(results) != 0 {
		t.Fatalf("expected cleared results, got %d", len(results))
	}
}

func TestServicePersistsJobs(t *testing.T) {
	svc, dir := newTestService(t)

	job, err := svc.CreateJob(CreateJobRequest{
		Name:            "Persist",
		Method:          http.MethodGet,
		URL:             "https://example.com/persist",
		IntervalSeconds: 60,
		Active:          false,
	})
	if err != nil {
		t.Fatalf("CreateJob returned error: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected job ID to be populated")
	}

	svc.Shutdown()

	svc2, err := NewService(Config{
		DataDir: dir,
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewService reload error: %v", err)
	}
	defer svc2.Shutdown()

	jobs := svc2.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job after reload, got %d", len(jobs))
	}
	if jobs[0].ID != job.ID {
		t.Fatalf("expected job ID %s after reload, got %s", job.ID, jobs[0].ID)
	}
}
