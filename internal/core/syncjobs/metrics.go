package syncjobs

import "time"

// SyncMetrics receives sync-queue observations from the worker. The hub's
// Prometheus exporter satisfies it. A nil metrics sink disables recording;
// the worker nil-checks before every call. Decouples the engine from any
// particular metrics backend.
type SyncMetrics interface {
	// ObserveSyncJob records one finished step and its duration.
	ObserveSyncJob(jobType, service, state string, d time.Duration)
	// ObserveSyncRetry records a retried or terminal failure.
	ObserveSyncRetry(jobType, class string)
	// SetSyncQueueDepth refreshes queue-depth gauges from the store rollup.
	SetSyncQueueDepth(c SyncCounts)
	// NoteSyncSuccess records the last successful finish per kind|service.
	NoteSyncSuccess(kind, service string, at time.Time)
}
