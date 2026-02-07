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
