package s3

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func gaugeValue(g prometheus.Gauge) float64 {
	m := &dto.Metric{}
	_ = g.Write(m)
	return m.GetGauge().GetValue()
}

func TestUpdateVolumeMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := InitS3Metrics(reg)
	if metrics == nil {
		t.Fatal("expected non-nil metrics")
	}

	metrics.UpdateVolumeMetrics(1000, 600, 400)

	if v := gaugeValue(metrics.VolumeTotalBytes); v != 1000 {
		t.Errorf("VolumeTotalBytes = %v, want 1000", v)
	}
	if v := gaugeValue(metrics.VolumeUsedBytes); v != 600 {
		t.Errorf("VolumeUsedBytes = %v, want 600", v)
	}
	if v := gaugeValue(metrics.VolumeAvailableBytes); v != 400 {
		t.Errorf("VolumeAvailableBytes = %v, want 400", v)
	}

	// Update with new values
	metrics.UpdateVolumeMetrics(2000, 1500, 500)

	if v := gaugeValue(metrics.VolumeTotalBytes); v != 2000 {
		t.Errorf("VolumeTotalBytes = %v, want 2000", v)
	}
	if v := gaugeValue(metrics.VolumeUsedBytes); v != 1500 {
		t.Errorf("VolumeUsedBytes = %v, want 1500", v)
	}
	if v := gaugeValue(metrics.VolumeAvailableBytes); v != 500 {
		t.Errorf("VolumeAvailableBytes = %v, want 500", v)
	}
}
