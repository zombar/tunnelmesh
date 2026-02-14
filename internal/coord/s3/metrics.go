package s3

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// s3MetricsOnce ensures metrics are only initialized once.
var s3MetricsOnce sync.Once

// s3MetricsInstance is the singleton instance of S3 metrics.
var s3MetricsInstance *S3Metrics

// S3Metrics holds all Prometheus metrics for the S3 service.
type S3Metrics struct {
	// Request metrics
	RequestsTotal   *prometheus.CounterVec   // tunnelmesh_s3_requests_total{operation,status}
	RequestDuration *prometheus.HistogramVec // tunnelmesh_s3_request_duration_seconds{operation}

	// Transfer metrics
	BytesUploaded   prometheus.Counter // tunnelmesh_s3_bytes_uploaded_total
	BytesDownloaded prometheus.Counter // tunnelmesh_s3_bytes_downloaded_total

	// Storage metrics
	BucketsTotal prometheus.Gauge // tunnelmesh_s3_buckets_total
	ObjectsTotal prometheus.Gauge // tunnelmesh_s3_objects_total
	StorageBytes prometheus.Gauge // tunnelmesh_s3_storage_bytes
	QuotaBytes   prometheus.Gauge // tunnelmesh_s3_quota_bytes (0 = unlimited)
	QuotaUsedPct prometheus.Gauge // tunnelmesh_s3_quota_used_percent

	// User metrics
	RegisteredUsers prometheus.Gauge // tunnelmesh_s3_registered_users

	// CAS/Chunking metrics
	ChunksTotal       prometheus.Gauge // tunnelmesh_s3_chunks_total
	ChunkStorageBytes prometheus.Gauge // tunnelmesh_s3_chunk_storage_bytes (actual on-disk after dedup)
	LogicalBytes      prometheus.Gauge // tunnelmesh_s3_logical_bytes (total without dedup)
	DedupRatio        prometheus.Gauge // tunnelmesh_s3_dedup_ratio (logical/physical, >1 means savings)
	VersionsTotal     prometheus.Gauge // tunnelmesh_s3_versions_total

	// GC metrics (counters for cumulative totals)
	GCRunsTotal       prometheus.Counter   // tunnelmesh_s3_gc_runs_total
	GCVersionsPruned  prometheus.Counter   // tunnelmesh_s3_gc_versions_pruned_total
	GCChunksDeleted   prometheus.Counter   // tunnelmesh_s3_gc_chunks_deleted_total
	GCBytesReclaimed  prometheus.Counter   // tunnelmesh_s3_gc_bytes_reclaimed_total
	GCDurationSeconds prometheus.Histogram // tunnelmesh_s3_gc_duration_seconds

	// Volume metrics (filesystem-level)
	VolumeTotalBytes     prometheus.Gauge // tunnelmesh_s3_volume_total_bytes
	VolumeUsedBytes      prometheus.Gauge // tunnelmesh_s3_volume_used_bytes
	VolumeAvailableBytes prometheus.Gauge // tunnelmesh_s3_volume_available_bytes
}

// InitS3Metrics initializes all S3 metrics.
// Metrics are only registered once; subsequent calls return the same instance.
func InitS3Metrics(registry prometheus.Registerer) *S3Metrics {
	s3MetricsOnce.Do(func() {
		if registry == nil {
			registry = prometheus.DefaultRegisterer
		}
		s3MetricsInstance = &S3Metrics{
			RequestsTotal: promauto.With(registry).NewCounterVec(prometheus.CounterOpts{
				Name: "tunnelmesh_s3_requests_total",
				Help: "Total S3 requests by operation and status",
			}, []string{"operation", "status"}),

			RequestDuration: promauto.With(registry).NewHistogramVec(prometheus.HistogramOpts{
				Name:    "tunnelmesh_s3_request_duration_seconds",
				Help:    "S3 request duration in seconds",
				Buckets: prometheus.DefBuckets,
			}, []string{"operation"}),

			BytesUploaded: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_s3_bytes_uploaded_total",
				Help: "Total bytes uploaded to S3",
			}),

			BytesDownloaded: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_s3_bytes_downloaded_total",
				Help: "Total bytes downloaded from S3",
			}),

			BucketsTotal: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_buckets_total",
				Help: "Total number of S3 buckets",
			}),

			ObjectsTotal: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_objects_total",
				Help: "Total number of S3 objects",
			}),

			StorageBytes: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_storage_bytes",
				Help: "Total bytes stored in S3",
			}),

			QuotaBytes: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_quota_bytes",
				Help: "S3 storage quota in bytes (0 = unlimited)",
			}),

			QuotaUsedPct: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_quota_used_percent",
				Help: "Percentage of S3 quota used",
			}),

			RegisteredUsers: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_registered_users",
				Help: "Number of registered S3 users",
			}),

			// CAS/Chunking metrics
			ChunksTotal: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_chunks_total",
				Help: "Total number of content-addressed chunks",
			}),

			ChunkStorageBytes: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_chunk_storage_bytes",
				Help: "Actual bytes stored in chunks (after deduplication)",
			}),

			LogicalBytes: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_logical_bytes",
				Help: "Logical bytes stored (before deduplication)",
			}),

			DedupRatio: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_dedup_ratio",
				Help: "Deduplication ratio (logical/physical bytes, >1 means space savings)",
			}),

			VersionsTotal: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_versions_total",
				Help: "Total number of object versions",
			}),

			// GC metrics
			GCRunsTotal: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_s3_gc_runs_total",
				Help: "Total number of garbage collection runs",
			}),

			GCVersionsPruned: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_s3_gc_versions_pruned_total",
				Help: "Total number of versions pruned by garbage collection",
			}),

			GCChunksDeleted: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_s3_gc_chunks_deleted_total",
				Help: "Total number of orphaned chunks deleted by garbage collection",
			}),

			GCBytesReclaimed: promauto.With(registry).NewCounter(prometheus.CounterOpts{
				Name: "tunnelmesh_s3_gc_bytes_reclaimed_total",
				Help: "Total bytes reclaimed by garbage collection",
			}),

			GCDurationSeconds: promauto.With(registry).NewHistogram(prometheus.HistogramOpts{
				Name:    "tunnelmesh_s3_gc_duration_seconds",
				Help:    "Garbage collection duration in seconds",
				Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120},
			}),

			// Volume metrics
			VolumeTotalBytes: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_volume_total_bytes",
				Help: "Total filesystem capacity in bytes",
			}),

			VolumeUsedBytes: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_volume_used_bytes",
				Help: "Used filesystem space in bytes",
			}),

			VolumeAvailableBytes: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
				Name: "tunnelmesh_s3_volume_available_bytes",
				Help: "Available filesystem space in bytes (non-root)",
			}),
		}
	})

	return s3MetricsInstance
}

// GetS3Metrics returns the singleton S3 metrics instance.
// Returns nil if metrics have not been initialized.
func GetS3Metrics() *S3Metrics {
	return s3MetricsInstance
}

// RecordRequest records a request metric.
func (m *S3Metrics) RecordRequest(operation string, status string, durationSeconds float64) {
	m.RequestsTotal.WithLabelValues(operation, status).Inc()
	m.RequestDuration.WithLabelValues(operation).Observe(durationSeconds)
}

// RecordUpload records bytes uploaded.
func (m *S3Metrics) RecordUpload(bytes int64) {
	m.BytesUploaded.Add(float64(bytes))
}

// RecordDownload records bytes downloaded.
func (m *S3Metrics) RecordDownload(bytes int64) {
	m.BytesDownloaded.Add(float64(bytes))
}

// UpdateStorageMetrics updates storage-related gauges.
func (m *S3Metrics) UpdateStorageMetrics(buckets, objects int, storageBytes, quotaBytes int64) {
	m.BucketsTotal.Set(float64(buckets))
	m.ObjectsTotal.Set(float64(objects))
	m.StorageBytes.Set(float64(storageBytes))
	m.QuotaBytes.Set(float64(quotaBytes))

	if quotaBytes > 0 {
		m.QuotaUsedPct.Set(float64(storageBytes) / float64(quotaBytes) * 100)
	} else {
		m.QuotaUsedPct.Set(0)
	}
}

// SetRegisteredUsers updates the registered users gauge.
func (m *S3Metrics) SetRegisteredUsers(count int) {
	m.RegisteredUsers.Set(float64(count))
}

// UpdateCASMetrics updates content-addressed storage metrics.
func (m *S3Metrics) UpdateCASMetrics(chunks int, chunkBytes, logicalBytes int64, versions int) {
	m.ChunksTotal.Set(float64(chunks))
	m.ChunkStorageBytes.Set(float64(chunkBytes))
	m.LogicalBytes.Set(float64(logicalBytes))
	m.VersionsTotal.Set(float64(versions))

	// Calculate dedup ratio (logical/physical)
	// A ratio > 1 means we're saving space through deduplication
	if chunkBytes > 0 {
		m.DedupRatio.Set(float64(logicalBytes) / float64(chunkBytes))
	} else {
		m.DedupRatio.Set(1.0)
	}
}

// UpdateVolumeMetrics updates filesystem volume gauges.
func (m *S3Metrics) UpdateVolumeMetrics(totalBytes, usedBytes, availableBytes int64) {
	m.VolumeTotalBytes.Set(float64(totalBytes))
	m.VolumeUsedBytes.Set(float64(usedBytes))
	m.VolumeAvailableBytes.Set(float64(availableBytes))
}

// RecordGCRun records garbage collection metrics.
func (m *S3Metrics) RecordGCRun(versionsPruned, chunksDeleted int, bytesReclaimed int64, durationSeconds float64) {
	m.GCRunsTotal.Inc()
	m.GCVersionsPruned.Add(float64(versionsPruned))
	m.GCChunksDeleted.Add(float64(chunksDeleted))
	m.GCBytesReclaimed.Add(float64(bytesReclaimed))
	m.GCDurationSeconds.Observe(durationSeconds)
}
