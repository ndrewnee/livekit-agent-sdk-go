package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

// TestJobQueue tests basic queue operations
func TestJobQueue(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{
		MaxSize: 10,
	})
	defer queue.Close()

	// Test enqueue
	job1 := &livekit.Job{
		Id:   "job-1",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-1"},
	}

	err := queue.Enqueue(job1, JobPriorityNormal, "token-1", "url-1")
	assert.NoError(t, err)
	assert.Equal(t, 1, queue.Size())

	// Test dequeue
	item, ok := queue.Dequeue()
	assert.True(t, ok)
	assert.NotNil(t, item)
	assert.Equal(t, "job-1", item.Job.Id)
	assert.Equal(t, JobPriorityNormal, item.Priority)
	assert.Equal(t, "token-1", item.Token)
	assert.Equal(t, "url-1", item.URL)
	assert.Equal(t, 0, queue.Size())

	// Test dequeue from empty queue
	item, ok = queue.Dequeue()
	assert.False(t, ok)
	assert.Nil(t, item)
}

// TestJobQueuePriority tests priority ordering
func TestJobQueuePriority(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{
		MaxSize: 10,
	})
	defer queue.Close()

	// Add jobs with different priorities
	jobs := []struct {
		id       string
		priority JobPriority
	}{
		{"low-1", JobPriorityLow},
		{"urgent-1", JobPriorityUrgent},
		{"normal-1", JobPriorityNormal},
		{"high-1", JobPriorityHigh},
		{"normal-2", JobPriorityNormal},
		{"urgent-2", JobPriorityUrgent},
	}

	for _, j := range jobs {
		job := &livekit.Job{
			Id:   j.id,
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room"},
		}
		err := queue.Enqueue(job, j.priority, "token", "url")
		assert.NoError(t, err)
	}

	// Dequeue should return in priority order
	expectedOrder := []string{"urgent-1", "urgent-2", "high-1", "normal-1", "normal-2", "low-1"}

	for _, expected := range expectedOrder {
		item, ok := queue.Dequeue()
		assert.True(t, ok)
		assert.Equal(t, expected, item.Job.Id)
	}
}

// TestJobQueueMaxSize tests queue size limits
func TestJobQueueMaxSize(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{
		MaxSize: 2,
	})
	defer queue.Close()

	// Fill the queue
	job1 := &livekit.Job{Id: "job-1", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: "room"}}
	job2 := &livekit.Job{Id: "job-2", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: "room"}}
	job3 := &livekit.Job{Id: "job-3", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: "room"}}

	assert.NoError(t, queue.Enqueue(job1, JobPriorityNormal, "token", "url"))
	assert.NoError(t, queue.Enqueue(job2, JobPriorityNormal, "token", "url"))

	// Third job should fail
	err := queue.Enqueue(job3, JobPriorityNormal, "token", "url")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "queue is full")
}

// TestJobQueueDequeueWithContext tests context-aware dequeuing
func TestJobQueueDequeueWithContext(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{})
	defer queue.Close()

	// Test immediate return when job available
	job := &livekit.Job{Id: "job-1", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: "room"}}
	err := queue.Enqueue(job, JobPriorityNormal, "token", "url")
	assert.NoError(t, err)

	ctx := context.Background()
	item, err := queue.DequeueWithContext(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, item)
	assert.Equal(t, "job-1", item.Job.Id)

	// Test context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	item, err = queue.DequeueWithContext(ctx)
	duration := time.Since(start)

	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)
	assert.Nil(t, item)
	assert.Less(t, duration, 100*time.Millisecond)
}

// TestJobQueuePeek tests peeking at the queue
func TestJobQueuePeek(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{})
	defer queue.Close()

	// Peek empty queue
	item, ok := queue.Peek()
	assert.False(t, ok)
	assert.Nil(t, item)

	// Add job and peek
	job := &livekit.Job{Id: "job-1", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: "room"}}
	err := queue.Enqueue(job, JobPriorityHigh, "token", "url")
	assert.NoError(t, err)

	item, ok = queue.Peek()
	assert.True(t, ok)
	assert.NotNil(t, item)
	assert.Equal(t, "job-1", item.Job.Id)

	// Verify size unchanged
	assert.Equal(t, 1, queue.Size())
}

// TestJobQueueRemoveJob tests removing specific jobs
func TestJobQueueRemoveJob(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{})
	defer queue.Close()

	// Add multiple jobs
	for i := 1; i <= 3; i++ {
		job := &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room"},
		}
		err := queue.Enqueue(job, JobPriorityNormal, "token", "url")
		assert.NoError(t, err)
	}

	assert.Equal(t, 3, queue.Size())

	// Remove middle job
	removed := queue.RemoveJob("job-2")
	assert.True(t, removed)
	assert.Equal(t, 2, queue.Size())

	// Try to remove non-existent job
	removed = queue.RemoveJob("job-99")
	assert.False(t, removed)

	// Verify remaining jobs
	item1, _ := queue.Dequeue()
	item2, _ := queue.Dequeue()
	assert.Equal(t, "job-1", item1.Job.Id)
	assert.Equal(t, "job-3", item2.Job.Id)
}

// TestJobQueueGetJobsByPriority tests filtering by priority
func TestJobQueueGetJobsByPriority(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{})
	defer queue.Close()

	// Add jobs with different priorities
	priorities := []JobPriority{
		JobPriorityLow, JobPriorityNormal, JobPriorityHigh,
		JobPriorityNormal, JobPriorityUrgent, JobPriorityHigh,
	}

	for i, priority := range priorities {
		job := &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room"},
		}
		err := queue.Enqueue(job, priority, "token", "url")
		assert.NoError(t, err)
	}

	// Check counts by priority
	assert.Len(t, queue.GetJobsByPriority(JobPriorityLow), 1)
	assert.Len(t, queue.GetJobsByPriority(JobPriorityNormal), 2)
	assert.Len(t, queue.GetJobsByPriority(JobPriorityHigh), 2)
	assert.Len(t, queue.GetJobsByPriority(JobPriorityUrgent), 1)
}

// TestDefaultPriorityCalculator tests default priority calculation
func TestDefaultPriorityCalculator(t *testing.T) {
	calc := &DefaultPriorityCalculator{}

	// Test metadata-based priority
	job := &livekit.Job{
		Id:       "job-1",
		Type:     livekit.JobType_JT_ROOM,
		Metadata: "urgent",
	}
	assert.Equal(t, JobPriorityUrgent, calc.CalculatePriority(job))

	// Test job type based priority
	roomJob := &livekit.Job{
		Id:   "job-2",
		Type: livekit.JobType_JT_ROOM,
	}
	assert.Equal(t, JobPriorityHigh, calc.CalculatePriority(roomJob))

	participantJob := &livekit.Job{
		Id:   "job-3",
		Type: livekit.JobType_JT_PARTICIPANT,
	}
	assert.Equal(t, JobPriorityNormal, calc.CalculatePriority(participantJob))
}

// TestMetadataPriorityCalculator tests metadata-based priority calculation
func TestMetadataPriorityCalculator(t *testing.T) {
	calc := &MetadataPriorityCalculator{
		PriorityField: "priority",
	}

	// Test different priority values
	tests := []struct {
		metadata string
		expected JobPriority
	}{
		{"priority:urgent", JobPriorityUrgent},
		{"priority:high", JobPriorityHigh},
		{"priority:low", JobPriorityLow},
		{"other:value", JobPriorityNormal},
		{"", JobPriorityNormal},
	}

	for _, test := range tests {
		job := &livekit.Job{
			Id:       "job",
			Type:     livekit.JobType_JT_ROOM,
			Metadata: test.metadata,
		}
		assert.Equal(t, test.expected, calc.CalculatePriority(job), "metadata: %s", test.metadata)
	}
}

// TestJobQueueConcurrency tests concurrent operations
func TestJobQueueConcurrency(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{
		MaxSize: 100,
	})
	defer queue.Close()

	// Concurrent enqueue
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			job := &livekit.Job{
				Id:   fmt.Sprintf("job-%d", id),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "room"},
			}
			priority := JobPriority(id % 4)
			err := queue.Enqueue(job, priority, "token", "url")
			assert.NoError(t, err)
			done <- true
		}(i)
	}

	// Wait for all enqueues
	for i := 0; i < 10; i++ {
		<-done
	}

	assert.Equal(t, 10, queue.Size())

	// Concurrent dequeue
	results := make(chan string, 10)
	for i := 0; i < 10; i++ {
		go func() {
			if item, ok := queue.Dequeue(); ok {
				results <- item.Job.Id
			}
		}()
	}

	// Collect results
	dequeued := make(map[string]bool)
	for i := 0; i < 10; i++ {
		id := <-results
		dequeued[id] = true
	}

	assert.Len(t, dequeued, 10)
	assert.Equal(t, 0, queue.Size())
}

// TestJobQueueClose tests queue closure
func TestJobQueueClose(t *testing.T) {
	queue := NewJobQueue(JobQueueOptions{})

	// Add a job
	job := &livekit.Job{Id: "job-1", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: "room"}}
	assert.NoError(t, queue.Enqueue(job, JobPriorityNormal, "token", "url"))

	// Close the queue
	queue.Close()

	// Try to enqueue after close
	err := queue.Enqueue(job, JobPriorityNormal, "token", "url")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "queue is closed")
}
