package provider

import (
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

func TestPMMServerHostFromURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{name: "http url", rawURL: "http://pmm.example.invalid", want: "pmm.example.invalid"},
		{name: "https with port", rawURL: "https://pmm.example.invalid:443/path", want: "pmm.example.invalid:443"},
		{name: "host without scheme", rawURL: "pmm.example.invalid/", want: "pmm.example.invalid"},
		{name: "empty", rawURL: "", wantErr: true},
		{name: "missing host", rawURL: "http://", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := pmmServerHostFromURL(tt.rawURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMonitoringConfigNameFromComponent(t *testing.T) {
	t.Parallel()

	name, err := monitoringConfigNameFromComponent(corev1alpha1.ComponentSpec{
		Type: common.MonitoringTypePMM,
		Parameters: &runtime.RawExtension{
			Raw: []byte(`{"monitoringConfigName":"test-pmm"}`),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "test-pmm" {
		t.Fatalf("got %q", name)
	}

	name, err = monitoringConfigNameFromComponent(corev1alpha1.ComponentSpec{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "" {
		t.Fatalf("got %q, want empty", name)
	}
}
