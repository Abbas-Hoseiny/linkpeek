//go:build legacy
// +build legacy

package scanner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}
	if mgr.httpClient == nil {
		t.Error("expected default HTTP client to be set")
	}
}

func TestCreateJob(t *testing.T) {
	mgr := NewManager(nil, nil, nil)

	job, err := mgr.CreateJob("Test Job", "GET", "https://example.com", 60, "", "")
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if job.ID == "" {
		t.Error("expected non-empty job ID")
	}
	if job.Name != "Test Job" {
		t.Errorf("expected name 'Test Job', got %s", job.Name)
	}
	if job.Method != "GET" {
		t.Errorf("expected method GET, got %s", job.Method)
	}
	if job.IntervalSeconds != 60 {
		t.Errorf("expected interval 60, got %d", job.IntervalSeconds)
	}
	if !job.Active {
		t.Error("expected job to be active after creation")
	}

	// Clean up
	mgr.DeleteJob(job.ID)
}

func TestCreateJobValidation(t *testing.T) {
	mgr := NewManager(nil, nil, nil)

	// Empty name
	_, err := mgr.CreateJob("", "GET", "https://example.com", 60, "", "")
	if err == nil {
		t.Error("expected error for empty job name")
	}

	// Empty URL
	_, err = mgr.CreateJob("Test", "GET", "", 60, "", "")
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestGetJob(t *testing.T) {
	mgr := NewManager(nil, nil, nil)

	job, _ := mgr.CreateJob("Test Job", "GET", "https://example.com", 60, "", "")

	retrieved, ok := mgr.GetJob(job.ID)
	if !ok {
		t.Error("expected job to be found")
	}
	if retrieved.ID != job.ID {
		t.Errorf("expected job ID %s, got %s", job.ID, retrieved.ID)
	}

	// Try to get non-existent job
	_, ok = mgr.GetJob("nonexistent")
	if ok {
		t.Error("expected job not to be found")
	}

	// Clean up
	mgr.DeleteJob(job.ID)
}

func TestListJobs(t *testing.T) {
	mgr := NewManager(nil, nil, nil)

	job1, _ := mgr.CreateJob("Job 1", "GET", "https://example.com/1", 60, "", "")
	job2, _ := mgr.CreateJob("Job 2", "POST", "https://example.com/2", 120, "", "")

	jobs := mgr.ListJobs()
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(jobs))
	}

	// Clean up
	mgr.DeleteJob(job1.ID)
	mgr.DeleteJob(job2.ID)
}

func TestDeleteJob(t *testing.T) {
	mgr := NewManager(nil, nil, nil)

	job, _ := mgr.CreateJob("Test Job", "GET", "https://example.com", 60, "", "")

	err := mgr.DeleteJob(job.ID)
	if err != nil {
		t.Errorf("DeleteJob failed: %v", err)
	}

	_, ok := mgr.GetJob(job.ID)
	if ok {
		t.Error("expected job to be deleted")
	}

	// Try to delete non-existent job
	err = mgr.DeleteJob("nonexistent")
	if err == nil {
		t.Error("expected error when deleting non-existent job")
	}
}

func TestStopJob(t *testing.T) {
	mgr := NewManager(nil, nil, nil)

	job, _ := mgr.CreateJob("Test Job", "GET", "https://example.com", 60, "", "")

	// Stop the job
	mgr.StopJob(job.ID)

	// Give it time to stop
	time.Sleep(50 * time.Millisecond)

	retrieved, _ := mgr.GetJob(job.ID)
	if retrieved.Active {
		t.Error("expected job to be inactive after stop")
	}

	// Clean up
	mgr.DeleteJob(job.ID)
}

func TestExecuteJob(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	}))
	defer server.Close()

	mgr := NewManager(http.DefaultClient, nil, nil)

	job, _ := mgr.CreateJob("Test Job", "GET", server.URL, 60, "", "")

	// Wait for the job to execute
	time.Sleep(200 * time.Millisecond)

	// Check results
	results := mgr.GetResults(10, job.ID)
	if len(results) == 0 {
		t.Error("expected at least one result")
	} else {
		result := results[0]
		if result.JobID != job.ID {
			t.Errorf("expected job ID %s, got %s", job.ID, result.JobID)
		}
		if result.Status != http.StatusOK {
			t.Errorf("expected status 200, got %d", result.Status)
		}
		if result.Error != "" {
			t.Errorf("expected no error, got %s", result.Error)
		}
	}

	// Check job status was updated
	retrieved, _ := mgr.GetJob(job.ID)
	if retrieved.LastStatus == "" {
		t.Error("expected job last status to be updated")
	}
	if retrieved.LastRun.IsZero() {
		t.Error("expected job last run to be updated")
	}

	// Clean up
	mgr.DeleteJob(job.ID)
}

func TestResultBuffer(t *testing.T) {
	buf := NewResultBuffer(5)

	// Add results
	for i := 0; i < 7; i++ {
		buf.Add(Result{
			ID:         fmt.Sprintf("res-%d", i),
			JobID:      "job-1",
			Timestamp:  time.Now(),
			Status:     200,
			DurationMs: int64(i * 100),
		})
	}

	// Should only keep last 5
	results := buf.Last(10, "")
	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}

	// Verify chronological order
	for i := 0; i < len(results)-1; i++ {
		if results[i].DurationMs > results[i+1].DurationMs {
			t.Error("results not in chronological order")
		}
	}
}

func TestResultBufferFiltering(t *testing.T) {
	buf := NewResultBuffer(10)

	// Add results for different jobs
	buf.Add(Result{ID: "1", JobID: "job-a", Status: 200})
	buf.Add(Result{ID: "2", JobID: "job-b", Status: 404})
	buf.Add(Result{ID: "3", JobID: "job-a", Status: 200})

	// Filter by job ID
	results := buf.Last(10, "job-a")
	if len(results) != 2 {
		t.Errorf("expected 2 results for job-a, got %d", len(results))
	}

	for _, r := range results {
		if r.JobID != "job-a" {
			t.Errorf("expected job ID job-a, got %s", r.JobID)
		}
	}
}

func TestShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	mgr := NewManager(http.DefaultClient, nil, nil)

	job1, _ := mgr.CreateJob("Job 1", "GET", server.URL, 60, "", "")
	job2, _ := mgr.CreateJob("Job 2", "GET", server.URL, 60, "", "")

	// Both jobs should be active
	j1, _ := mgr.GetJob(job1.ID)
	j2, _ := mgr.GetJob(job2.ID)
	if !j1.Active || !j2.Active {
		t.Error("expected both jobs to be active")
	}

	// Shutdown
	mgr.Shutdown()

	// Give time for shutdown
	time.Sleep(50 * time.Millisecond)

	// Both jobs should be inactive
	j1, _ = mgr.GetJob(job1.ID)
	j2, _ = mgr.GetJob(job2.ID)
	if j1.Active || j2.Active {
		t.Error("expected both jobs to be inactive after shutdown")
	}
}
