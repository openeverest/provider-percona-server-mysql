package provider

import (
	"encoding/json"
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func engineWithConfiguration(t *testing.T, configuration string) corev1alpha1.ComponentSpec {
	t.Helper()

	raw, err := json.Marshal(map[string]any{"configuration": configuration})
	if err != nil {
		t.Fatalf("marshal configuration: %v", err)
	}

	return corev1alpha1.ComponentSpec{
		Parameters: &runtime.RawExtension{Raw: raw},
	}
}

func TestEngineConfigurationFromComponent_Empty(t *testing.T) {
	t.Parallel()

	got, err := engineConfigurationFromComponent(corev1alpha1.ComponentSpec{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}

func TestEngineConfigurationFromComponent_BlankConfiguration(t *testing.T) {
	t.Parallel()

	got, err := engineConfigurationFromComponent(engineWithConfiguration(t, "   "))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}

func TestEngineConfigurationFromComponent_ParsesArbitraryParameters(t *testing.T) {
	t.Parallel()

	engine := engineWithConfiguration(t, `
max_connections: "500"
innodb_buffer_pool_size: 1G
some_future_parameter: true
`)

	got, err := engineConfigurationFromComponent(engine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["max_connections"] != "500" {
		t.Fatalf("max_connections = %#v", got["max_connections"])
	}
	if got["innodb_buffer_pool_size"] != "1G" {
		t.Fatalf("innodb_buffer_pool_size = %#v", got["innodb_buffer_pool_size"])
	}
	if got["some_future_parameter"] != true {
		t.Fatalf("some_future_parameter = %#v", got["some_future_parameter"])
	}
}

func TestRenderMySQLConfiguration(t *testing.T) {
	t.Parallel()

	engine := engineWithConfiguration(t, `
max_connections: "500"
innodb_buffer_pool_size: 1G
innodb_flush_log_at_trx_commit: 2
log_output: "FILE"
`)

	params, err := engineConfigurationFromComponent(engine)
	if err != nil {
		t.Fatalf("parse configuration: %v", err)
	}
	got, err := renderMySQLConfiguration(params)
	if err != nil {
		t.Fatalf("render configuration: %v", err)
	}
	want := "innodb_buffer_pool_size=\"1G\"\ninnodb_flush_log_at_trx_commit=2\nlog_output=\"FILE\"\nmax_connections=\"500\""
	if got != want {
		t.Fatalf("rendered configuration:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyEngineConfiguration(t *testing.T) {
	t.Parallel()

	cluster := &psv1.PerconaServerMySQL{}
	engine := engineWithConfiguration(t, "max_connections: \"250\"\n")
	if err := applyEngineConfiguration(cluster, engine); err != nil {
		t.Fatalf("apply configuration: %v", err)
	}
	if cluster.Spec.MySQL.Configuration != `max_connections="250"` {
		t.Fatalf("configuration = %q", cluster.Spec.MySQL.Configuration)
	}
}

func TestApplyEngineConfiguration_ClearsWhenUnset(t *testing.T) {
	t.Parallel()

	cluster := &psv1.PerconaServerMySQL{}
	cluster.Spec.MySQL.Configuration = "max_connections=250"
	if err := applyEngineConfiguration(cluster, corev1alpha1.ComponentSpec{}); err != nil {
		t.Fatalf("apply configuration: %v", err)
	}
	if cluster.Spec.MySQL.Configuration != "" {
		t.Fatalf("configuration = %q, want empty", cluster.Spec.MySQL.Configuration)
	}
}

func TestEngineConfigurationFromComponent_InvalidYAML(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain scalar (not a mapping)": "afsdasdfasd",
		"list instead of a mapping":    "- a\n- b\n",
		"bad indentation":              "foo: bar\n  baz: qux\n",
		"tab indentation":              "foo:\n\tbar: baz\n",
	}

	for name, configuration := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := engineConfigurationFromComponent(engineWithConfiguration(t, configuration))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
