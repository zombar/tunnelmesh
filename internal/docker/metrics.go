package docker

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	once            sync.Once
	metricsRegistry *dockerMetrics
)

// dockerMetrics holds Prometheus metrics for Docker containers.
type dockerMetrics struct {
	cpuPercent      *prometheus.GaugeVec
	memoryBytes     *prometheus.GaugeVec
	memoryLimit     *prometheus.GaugeVec
	memoryPercent   *prometheus.GaugeVec
	diskBytes       *prometheus.GaugeVec
	pids            *prometheus.GaugeVec
	containerInfo   *prometheus.GaugeVec
	containerStatus *prometheus.GaugeVec
}

// initMetrics initializes Docker Prometheus metrics (singleton).
func initMetrics(peerName string) *dockerMetrics {
	once.Do(func() {
		metricsRegistry = &dockerMetrics{
			cpuPercent: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_cpu_percent",
					Help: "CPU usage percentage for Docker container (0-100+ for multi-core)",
				},
				[]string{"peer", "container_id", "container_name", "image"},
			),
			memoryBytes: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_memory_bytes",
					Help: "Current memory usage in bytes for Docker container",
				},
				[]string{"peer", "container_id", "container_name", "image"},
			),
			memoryLimit: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_memory_limit_bytes",
					Help: "Memory limit in bytes for Docker container",
				},
				[]string{"peer", "container_id", "container_name", "image"},
			),
			memoryPercent: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_memory_percent",
					Help: "Memory usage percentage for Docker container",
				},
				[]string{"peer", "container_id", "container_name", "image"},
			),
			diskBytes: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_disk_bytes",
					Help: "Disk space used by Docker container in bytes",
				},
				[]string{"peer", "container_id", "container_name", "image"},
			),
			pids: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_pids",
					Help: "Number of processes in Docker container",
				},
				[]string{"peer", "container_id", "container_name", "image"},
			),
			containerInfo: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_info",
					Help: "Docker container information (always 1)",
				},
				[]string{"peer", "container_id", "container_name", "image", "status", "network_mode"},
			),
			containerStatus: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "docker_container_status",
					Help: "Docker container status (1=running, 0=stopped)",
				},
				[]string{"peer", "container_id", "container_name", "image"},
			),
		}
	})
	return metricsRegistry
}

// recordStats records Docker container stats to Prometheus.
func (m *Manager) recordStats(stats ContainerStats, containerInfo *ContainerInfo) {
	if metricsRegistry == nil {
		return
	}

	labels := prometheus.Labels{
		"peer":           m.peerName,
		"container_id":   stats.ContainerID,
		"container_name": stats.ContainerName,
		"image":          containerInfo.Image,
	}

	metricsRegistry.cpuPercent.With(labels).Set(stats.CPUPercent)
	metricsRegistry.memoryBytes.With(labels).Set(float64(stats.MemoryBytes))
	metricsRegistry.memoryLimit.With(labels).Set(float64(stats.MemoryLimit))
	metricsRegistry.memoryPercent.With(labels).Set(stats.MemoryPercent)
	metricsRegistry.diskBytes.With(labels).Set(float64(stats.DiskBytes))
	metricsRegistry.pids.With(labels).Set(float64(stats.PIDs))
}

// recordContainerInfo records Docker container info to Prometheus.
func (m *Manager) recordContainerInfo(container *ContainerInfo) {
	if metricsRegistry == nil {
		return
	}

	infoLabels := prometheus.Labels{
		"peer":           m.peerName,
		"container_id":   container.ID,
		"container_name": container.Name,
		"image":          container.Image,
		"status":         container.Status,
		"network_mode":   container.NetworkMode,
	}

	metricsRegistry.containerInfo.With(infoLabels).Set(1)

	statusLabels := prometheus.Labels{
		"peer":           m.peerName,
		"container_id":   container.ID,
		"container_name": container.Name,
		"image":          container.Image,
	}

	statusValue := 0.0
	if container.State == "running" {
		statusValue = 1.0
	}
	metricsRegistry.containerStatus.With(statusLabels).Set(statusValue)
}
