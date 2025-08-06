package jobs

import (
	"testing"
	"time"

	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/tracing"
	"github.com/grafana/grafana/pkg/services/ngalert/lokiclient"
	"github.com/grafana/grafana/pkg/services/ngalert/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	provisioning "github.com/grafana/grafana/apps/provisioning/pkg/apis/provisioning/v0alpha1"
)

func TestLokiJobHistory_WriteJob(t *testing.T) {
	// Create test job
	job := &provisioning.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-job",
			Namespace:         "test-namespace",
			UID:               types.UID("test-uid"),
			CreationTimestamp: metav1.NewTime(time.Now()),
			Labels: map[string]string{
				"test": "label",
				LabelJobClaim: "should-be-removed",
			},
		},
		Spec: provisioning.JobSpec{
			Action:     provisioning.JobActionPull,
			Repository: "test-repo",
		},
		Status: provisioning.JobStatus{
			State:   provisioning.JobStateSuccess,
			Started: time.Now().Unix() - 100,
			Finished: time.Now().Unix(),
			Message: "Job completed successfully",
		},
	}

	t.Run("jobToStream creates correct stream", func(t *testing.T) {
		history := createTestLokiJobHistory(t)
		logger := log.NewNopLogger()

		// Clean job copy like WriteJob does
		jobCopy := job.DeepCopy()
		delete(jobCopy.Labels, LabelJobClaim)

		stream := history.jobToStream(jobCopy, logger)

		// Verify labels
		assert.Equal(t, JobHistoryLabelValue, stream.Stream[JobHistoryLabelKey])
		assert.Equal(t, job.Namespace, stream.Stream[NamespaceLabel])
		assert.Equal(t, job.Spec.Repository, stream.Stream[RepositoryLabel])
		assert.Equal(t, "test-value", stream.Stream["test-key"]) // external label

		// Verify we have a sample
		require.Len(t, stream.Values, 1)
		
		// Verify timestamp (should use finished time)
		expectedTime := time.Unix(job.Status.Finished, 0)
		assert.Equal(t, expectedTime, stream.Values[0].T)

		// Verify job data is JSON
		assert.Contains(t, stream.Values[0].V, "test-job")
		assert.Contains(t, stream.Values[0].V, "test-namespace")
		assert.NotContains(t, stream.Values[0].V, LabelJobClaim) // should be cleaned
	})

	t.Run("buildJobQuery creates correct LogQL", func(t *testing.T) {
		history := createTestLokiJobHistory(t)

		query := history.buildJobQuery("test-ns", "test-repo")

		expected := `{from="job-history",namespace="test-ns",repository="test-repo"}`
		assert.Equal(t, expected, query)
	})

	t.Run("getJobTimestamp returns correct timestamp", func(t *testing.T) {
		history := createTestLokiJobHistory(t)

		// Test finished time priority
		jobWithFinished := &provisioning.Job{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Unix(100, 0)),
			},
			Status: provisioning.JobStatus{
				Started:  200,
				Finished: 300,
			},
		}
		ts := history.getJobTimestamp(jobWithFinished)
		assert.Equal(t, time.Unix(300, 0), ts)

		// Test started time when no finished time
		jobWithStarted := &provisioning.Job{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Unix(100, 0)),
			},
			Status: provisioning.JobStatus{
				Started: 200,
			},
		}
		ts = history.getJobTimestamp(jobWithStarted)
		assert.Equal(t, time.Unix(200, 0), ts)

		// Test creation time when no other timestamps
		jobWithCreation := &provisioning.Job{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Unix(100, 0)),
			},
		}
		ts = history.getJobTimestamp(jobWithCreation)
		assert.Equal(t, time.Unix(100, 0), ts)
	})
}

func TestLokiJobHistory_Integration(t *testing.T) {
	t.Run("loki job history can be created", func(t *testing.T) {
		history := createTestLokiJobHistory(t)
		
		// Should return Loki implementation
		_, ok := history.(*LokiJobHistory)
		assert.True(t, ok)
	})
}

// createTestLokiJobHistory creates a LokiJobHistory for testing
func createTestLokiJobHistory(t *testing.T) *LokiJobHistory {
	logger := log.NewNopLogger()
	config := lokiclient.LokiConfig{
		ExternalLabels: map[string]string{
			"test-key": "test-value",
		},
	}
	metrics := metrics.NewHistorianMetrics(prometheus.NewRegistry(), "test")
	tracer := tracing.InitializeTracerForTest()
	requester := lokiclient.NewFakeRequester()

	return NewLokiJobHistory(logger, config, requester, metrics, tracer)
}