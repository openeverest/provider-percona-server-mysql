package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const (
	monTestInstance    = "db"
	monTestNamespace   = "team-a"
	monTestUsersSecret = "db-secrets"
	monTestPMMImage    = "percona/pmm-client:3.8.0"
)

var monTestProviderSpec = corev1alpha1.ProviderSpec{
	ComponentTypes: map[string]corev1alpha1.ComponentType{
		common.ComponentTypeMySQLDExporter: {
			DefaultVersion: "0.20.0",
			Versions:       []corev1alpha1.ComponentVersion{{Version: "0.20.0", Image: "prom/mysqld-exporter:v0.20.0"}},
		},
	},
	Monitoring: &corev1alpha1.ProviderMonitoring{
		Integrations: map[string]corev1alpha1.MonitoringIntegration{pmmIntegration: {
			AgentVersions: ">=3.0.0 <4.0.0",
			AuthModes:     []string{"ServiceAccountToken"},
		}},
	},
}

// monitoringContext returns the context of an Instance listing entries in
// spec.monitoring; objs are what the cluster holds besides the Instance and
// its Provider.
func monitoringContext(t *testing.T, entries []corev1alpha1.InstanceMonitoringDestination, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, monitoringv1alpha1.AddToScheme(scheme))
	require.NoError(t, psv1.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: monTestInstance, Namespace: monTestNamespace, UID: "instance-uid"},
		Spec: corev1alpha1.InstanceSpec{
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentEngine: {Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				}},
			},
		},
	}
	if entries != nil {
		in.Spec.Monitoring = &corev1alpha1.InstanceMonitoringSpec{Destinations: entries}
	}
	provider := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: common.ProviderName},
		Spec:       monTestProviderSpec,
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, in, provider)...).Build()
	return controller.NewContext(context.Background(), cl, in, provider.Name)
}

func monEntry(name, destination string) corev1alpha1.InstanceMonitoringDestination {
	return corev1alpha1.InstanceMonitoringDestination{
		Name:           name,
		DestinationRef: commonv1alpha1.MonitoringDestinationRef{Name: destination},
	}
}

func monBindingName(entry string) string {
	return monTestInstance + "-" + entry
}

// monBinding is the binding core materialises for an entry, Accepted for its
// generation when accepted is set.
func monBinding(entry, destination, class string, mode corev1alpha1.MonitoringExecutionMode, accepted bool, parameters string) *monitoringv1alpha1.MonitoringBinding {
	b := &monitoringv1alpha1.MonitoringBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       monBindingName(entry),
			Namespace:  monTestNamespace,
			Generation: 1,
			Labels: map[string]string{
				monitoringv1alpha1.BindingInstanceLabel: monTestInstance,
				monitoringv1alpha1.BindingEntryLabel:    entry,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: corev1alpha1.GroupVersion.String(),
				Kind:       "Instance",
				Name:       monTestInstance,
				UID:        "instance-uid",
				Controller: new(true),
			}},
		},
		Spec: monitoringv1alpha1.MonitoringBindingSpec{
			InstanceRef:    commonv1alpha1.ObjectRef{Name: monTestInstance},
			DestinationRef: commonv1alpha1.MonitoringDestinationRef{Name: destination},
			ClassRef:       &commonv1alpha1.ObjectRef{Name: class},
			ExecutionMode:  mode,
		},
	}
	if parameters != "" {
		b.Spec.Parameters = &runtime.RawExtension{Raw: []byte(parameters)}
	}
	if accepted {
		b.Status.Conditions = []metav1.Condition{{
			Type:               monitoringv1alpha1.MonitoringBindingConditionAccepted,
			Status:             metav1.ConditionTrue,
			Reason:             monitoringv1alpha1.ReasonAccepted,
			ObservedGeneration: 1,
		}}
	}
	return b
}

func monPMMBinding(accepted bool, parameters string) *monitoringv1alpha1.MonitoringBinding {
	return monBinding("pmm", "pmm-prod", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged, accepted, parameters)
}

func monPrometheusBinding() *monitoringv1alpha1.MonitoringBinding {
	return monBinding("prometheus", "prometheus", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged, true, "")
}

func monPMMClass() *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: "pmm"},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ProviderManaged: &monitoringv1alpha1.ProviderManagedFulfilment{
				Integration:  pmmIntegration,
				AgentImage:   monTestPMMImage,
				AgentVersion: "3.8.0",
			},
			AuthModes: []commonv1alpha1.AuthMode{{
				Name:        "ServiceAccountToken",
				Credentials: &commonv1alpha1.CredentialKeys{Required: []string{pmmTokenKey}},
			}},
		},
	}
}

func monPrometheusClass() *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus"},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ExtensionManaged: &monitoringv1alpha1.ExtensionManagedFulfilment{
				Requires: monitoringv1alpha1.SourceRequirements{Metrics: true},
			},
			AuthModes: []commonv1alpha1.AuthMode{{Name: commonv1alpha1.AuthModeAnonymous}},
		},
	}
}

func monDestination(name, class, url string, authentication commonv1alpha1.Authentication) *monitoringv1alpha1.MonitoringDestination {
	dst := &monitoringv1alpha1.MonitoringDestination{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: monTestNamespace},
		Spec: monitoringv1alpha1.MonitoringDestinationSpec{
			ClassRef:       commonv1alpha1.ObjectRef{Name: class},
			Authentication: authentication,
		},
	}
	if url != "" {
		dst.Spec.Parameters = &runtime.RawExtension{Raw: []byte(`{"url":"` + url + `"}`)}
	}
	return dst
}

func monPrometheusDestination() *monitoringv1alpha1.MonitoringDestination {
	return monDestination("prometheus", "prometheus", "", commonv1alpha1.Authentication{Mode: commonv1alpha1.AuthModeAnonymous})
}

func monPMMAuthentication() commonv1alpha1.Authentication {
	return commonv1alpha1.Authentication{
		Mode:                 "ServiceAccountToken",
		CredentialsSecretRef: &commonv1alpha1.SecretRef{Name: "pmm-prod-credentials"},
	}
}

func monPMMDestination() *monitoringv1alpha1.MonitoringDestination {
	return monDestination("pmm-prod", "pmm", "https://pmm.example.com:8443", monPMMAuthentication())
}

func monSecret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: monTestNamespace},
		Data:       map[string][]byte{},
	}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func monPMMCredentials() *corev1.Secret {
	return monSecret("pmm-prod-credentials", map[string]string{pmmTokenKey: "glsa_token"})
}

// monLiveCluster is the engine CR as the operator currently holds it, with
// every pod ready when ready is true.
func monLiveCluster(ready bool, haproxy, exporter, pmm bool) *psv1.PerconaServerMySQL {
	cluster := &psv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: monTestInstance, Namespace: monTestNamespace},
		Spec: psv1.PerconaServerMySQLSpec{
			SecretsName: monTestUsersSecret,
		},
	}
	if haproxy {
		cluster.Spec.Proxy.HAProxy = &psv1.HAProxySpec{Enabled: true}
	}
	if ready {
		cluster.Status.MySQL.Ready = 3
		if haproxy {
			cluster.Status.HAProxy.Ready = 2
		}
	}
	if exporter {
		cluster.Spec.MySQL.Sidecars = []corev1.Container{{Name: mysqldExporterContainerName}}
	}
	if pmm {
		cluster.Spec.PMM = &psv1.PMMSpec{Enabled: true, ServerHost: "live-pmm.example.com", Image: "percona/pmm-client:3.7.0"}
	}
	return cluster
}

// monDesiredCluster is the engine CR Sync is building, before monitoring.
func monDesiredCluster() *psv1.PerconaServerMySQL {
	return &psv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: monTestInstance, Namespace: monTestNamespace},
		Spec:       psv1.PerconaServerMySQLSpec{SecretsName: monTestUsersSecret},
	}
}

func TestMonitoringSources(t *testing.T) {
	t.Parallel()

	engine := corev1alpha1.MetricsEndpoint{
		Component: common.ComponentEngine,
		Kind:      "mysql",
		PodSelector: map[string]string{
			controller.InstanceLabel:  monTestInstance,
			controller.ComponentLabel: common.ComponentEngine,
		},
		Port:   intstr.FromString(metricsPortName),
		Path:   "/metrics",
		Scheme: "http",
	}
	haproxy := corev1alpha1.MetricsEndpoint{
		Component: common.ComponentProxy,
		Kind:      "haproxy",
		PodSelector: map[string]string{
			controller.InstanceLabel:  monTestInstance,
			controller.ComponentLabel: common.ComponentProxy,
		},
		Port:   intstr.FromInt32(haproxyMetricsPort),
		Path:   "/metrics",
		Scheme: "http",
	}

	tests := []struct {
		name string
		objs []client.Object
		want []corev1alpha1.MetricsEndpoint
	}{
		{
			name: "HAProxy and the exporter sidecar",
			objs: []client.Object{monLiveCluster(true, true, true, false)},
			want: []corev1alpha1.MetricsEndpoint{engine, haproxy},
		},
		{
			name: "no engine endpoint without the sidecar",
			objs: []client.Object{monLiveCluster(true, true, false, true)},
			want: []corev1alpha1.MetricsEndpoint{haproxy},
		},
		{
			name: "nothing before a pod is ready",
			objs: []client.Object{monLiveCluster(false, true, true, false)},
		},
		{
			name: "nothing before the cluster exists",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContext(t, nil, tt.objs...)

			sources, err := (&Provider{}).MonitoringSources(c)
			require.NoError(t, err)
			assert.Equal(t, tt.want, sources.Metrics)
		})
	}
}

func TestMonitoringIntegrations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "definition", "monitoring.yaml"))
	require.NoError(t, err)
	definition := corev1alpha1.ProviderMonitoring{}
	require.NoError(t, yaml.Unmarshal(raw, &definition))

	keys := make([]string, 0, len(definition.Integrations))
	for name := range definition.Integrations {
		keys = append(keys, name)
	}
	assert.ElementsMatch(t, keys, (&Provider{}).MonitoringIntegrations())
}

func TestMetricsExporterFollowsRequiredSources(t *testing.T) {
	t.Parallel()

	prometheusEntry := []corev1alpha1.InstanceMonitoringDestination{monEntry("prometheus", "prometheus")}
	pmmEntry := []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")}

	tests := []struct {
		name    string
		entries []corev1alpha1.InstanceMonitoringDestination
		objs    []client.Object
		want    bool
	}{
		{
			name:    "a bound class requires metrics",
			entries: prometheusEntry,
			objs:    []client.Object{monPrometheusBinding(), monPrometheusDestination(), monPrometheusClass()},
			want:    true,
		},
		{
			name:    "only PMM is bound",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(true, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monLiveCluster(true, true, true, true),
			},
			want: false,
		},
		{
			name: "no destinations",
			objs: []client.Object{monLiveCluster(true, true, true, false)},
			want: false,
		},
		{
			name:    "an entry without a binding keeps the live sidecar",
			entries: prometheusEntry,
			objs:    []client.Object{monLiveCluster(true, true, true, false)},
			want:    true,
		},
		{
			name:    "an entry without a binding adds none",
			entries: prometheusEntry,
			objs:    []client.Object{monLiveCluster(true, true, false, false)},
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContext(t, tt.entries, tt.objs...)
			cluster := monDesiredCluster()

			require.NoError(t, applyMonitoring(c, cluster, &monTestProviderSpec))
			assert.Equal(t, tt.want, hasMetricsExporter(cluster))
		})
	}
}

func TestMetricsExporterSidecar(t *testing.T) {
	t.Parallel()

	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("prometheus", "prometheus")},
		monPrometheusBinding(), monPrometheusDestination(), monPrometheusClass())
	cluster := monDesiredCluster()

	require.NoError(t, applyMonitoring(c, cluster, &monTestProviderSpec))

	require.Len(t, cluster.Spec.MySQL.Sidecars, 1)
	sidecar := cluster.Spec.MySQL.Sidecars[0]
	assert.Equal(t, "prom/mysqld-exporter:v0.20.0", sidecar.Image, "the image comes from the provider's version catalog")
	assert.Equal(t, []string{"--mysqld.address=127.0.0.1:3306", "--mysqld.username=monitor"}, sidecar.Args)
	require.Len(t, sidecar.Env, 1)
	assert.Equal(t, &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: monTestUsersSecret},
		Key:                  "monitor",
	}, sidecar.Env[0].ValueFrom.SecretKeyRef, "the operator's monitor user password")
	assert.Equal(t, []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9104, Protocol: corev1.ProtocolTCP}}, sidecar.Ports)
}

func TestPMMFollowsRequiredSources(t *testing.T) {
	t.Parallel()

	pmmEntry := []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")}
	// livePMM documents the live spec.pmm value; each test case builds its
	// own cluster object (monLiveCluster), since the fake client mutates
	// whatever object it is given (e.g. ResourceVersion) and parallel
	// subtests must not share one.
	livePMM := monLiveCluster(true, true, false, true).Spec.PMM

	tests := []struct {
		name      string
		entries   []corev1alpha1.InstanceMonitoringDestination
		objs      []client.Object
		wantHost  string
		wantLive  bool
		wantNoPMM bool
		wantToken string
	}{
		{
			name:    "an Accepted binding is rendered",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(true, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, nil), monSecret("internal-"+monTestInstance, nil),
				monLiveCluster(true, true, false, true),
			},
			wantHost:  "pmm.example.com:8443",
			wantToken: "glsa_token",
		},
		{
			name:    "a binding that is not Accepted keeps the live spec.pmm",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(false, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token"}),
				monLiveCluster(true, true, false, true),
			},
			wantLive:  true,
			wantToken: "old_token",
		},
		{
			name:    "an entry without a binding enables nothing",
			entries: pmmEntry,
			objs: []client.Object{
				monSecret(monTestUsersSecret, nil),
				monLiveCluster(true, true, false, false),
			},
			wantNoPMM: true,
		},
		{
			name: "a removed entry disables PMM and drops the token mirror",
			objs: []client.Object{
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token", "root": "pw"}),
				monLiveCluster(true, true, false, true),
			},
			wantNoPMM: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContext(t, tt.entries, tt.objs...)
			cluster := monDesiredCluster()

			require.NoError(t, applyMonitoring(c, cluster, &monTestProviderSpec))

			switch {
			case tt.wantNoPMM:
				assert.Nil(t, cluster.Spec.PMM)
			case tt.wantLive:
				assert.Equal(t, livePMM, cluster.Spec.PMM)
			default:
				require.NotNil(t, cluster.Spec.PMM)
				assert.True(t, cluster.Spec.PMM.Enabled)
				assert.Equal(t, tt.wantHost, cluster.Spec.PMM.ServerHost)
			}

			users := &corev1.Secret{}
			require.NoError(t, c.Get(users, monTestUsersSecret))
			token := string(users.Data[pmmServerTokenKey])
			assert.Equal(t, tt.wantToken, token)
		})
	}
}

func TestPMMRender(t *testing.T) {
	t.Parallel()

	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")},
		monPMMBinding(true, `{"resources":{"requests":{"cpu":"500m"},"limits":{"memory":"512Mi"}}}`),
		monPMMDestination(), monPMMClass(), monPMMCredentials(),
		monSecret(monTestUsersSecret, map[string]string{"root": "pw"}),
		monSecret("internal-"+monTestInstance, nil),
	)
	cluster := monDesiredCluster()

	require.NoError(t, applyMonitoring(c, cluster, &monTestProviderSpec))

	require.NotNil(t, cluster.Spec.PMM)
	assert.Equal(t, "pmm.example.com:8443", cluster.Spec.PMM.ServerHost)
	assert.Equal(t, monTestPMMImage, cluster.Spec.PMM.Image)
	assert.Equal(t, monTestInstance, cluster.Spec.PMM.CustomClusterName)
	assert.Equal(t, corev1.PullIfNotPresent, cluster.Spec.PMM.ImagePullPolicy)
	assert.Equal(t, "--custom-labels=namespace=team-a,core_openeverest_io_instance=db,core_openeverest_io_component=engine", cluster.Spec.PMM.MySQLParams)

	users := &corev1.Secret{}
	require.NoError(t, c.Get(users, monTestUsersSecret))
	assert.Equal(t, "glsa_token", string(users.Data[pmmServerTokenKey]))
	assert.Equal(t, "pw", string(users.Data["root"]), "the operator's users are left alone")

	internal := &corev1.Secret{}
	require.NoError(t, c.Get(internal, "internal-"+monTestInstance))
	assert.Equal(t, "glsa_token", string(internal.Data[pmmServerTokenKey]), "the PS operator compares the token in both Secrets before enabling PMM")
}

func TestPMMRenderFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		destination *monitoringv1alpha1.MonitoringDestination
		credentials *corev1.Secret
		wantMessage string
	}{
		{
			name:        "destination without url",
			destination: monDestination("pmm-prod", "pmm", "", monPMMAuthentication()),
			credentials: monPMMCredentials(),
			wantMessage: "MonitoringDestination pmm-prod parameters: invalid parameters: url is required",
		},
		{
			name:        "missing credentials Secret",
			destination: monPMMDestination(),
			wantMessage: `get credentials Secret "pmm-prod-credentials" of MonitoringDestination "pmm-prod": secrets "pmm-prod-credentials" not found`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			objs := []client.Object{
				monPMMBinding(true, ""), tt.destination, monPMMClass(),
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token"}),
				monLiveCluster(true, true, false, true),
			}
			if tt.credentials != nil {
				objs = append(objs, tt.credentials)
			}
			c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")}, objs...)
			cluster := monDesiredCluster()

			require.NoError(t, applyMonitoring(c, cluster, &monTestProviderSpec), "a bad binding never fails Sync")

			reason, message, staged := c.MonitoringBindingResult(monBindingName("pmm"))
			require.True(t, staged)
			assert.Equal(t, monitoringv1alpha1.ReasonRenderFailed, reason)
			assert.Contains(t, message, tt.wantMessage)
			require.NotNil(t, cluster.Spec.PMM)
			assert.Equal(t, "live-pmm.example.com", cluster.Spec.PMM.ServerHost, "the live spec.pmm is kept")
		})
	}
}

func TestPMMAPIErrorsFailSync(t *testing.T) {
	t.Parallel()

	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")},
		monPMMBinding(true, ""), monDestination("pmm-prod", "pmm", "", monPMMAuthentication()), monPMMClass(), monPMMCredentials(),
		monSecret(monTestUsersSecret, nil),
		monLiveCluster(true, true, false, true),
	)

	err := applyMonitoring(c, monDesiredCluster(), &monTestProviderSpec)
	require.NoError(t, err)
}

func TestPMMResources(t *testing.T) {
	t.Parallel()

	engine := func(memory string) *corev1.ResourceRequirements {
		return &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memory)}}
	}

	tests := []struct {
		name    string
		engine  *corev1.ResourceRequirements
		entry   *corev1.ResourceRequirements
		want    corev1.ResourceRequirements
		wantErr string
	}{
		{
			name:   "small engine",
			engine: engine("2Gi"),
			want:   corev1.ResourceRequirements{Requests: pmmRequestsSmall},
		},
		{
			name:   "medium engine",
			engine: engine("8Gi"),
			want:   corev1.ResourceRequirements{Requests: pmmRequestsMedium},
		},
		{
			name:   "large engine",
			engine: engine("32Gi"),
			want:   corev1.ResourceRequirements{Requests: pmmRequestsLarge},
		},
		{
			name: "no engine resources",
			want: corev1.ResourceRequirements{Requests: pmmRequestsSmall},
		},
		{
			name:   "entry request above its own limit",
			engine: engine("2Gi"),
			entry: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			},
			wantErr: "resources.requests.memory (1Gi) is above resources.limits.memory (512Mi)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := pmmResources(tt.engine, tt.entry)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
