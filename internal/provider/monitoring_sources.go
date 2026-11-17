package provider

import (
	"errors"
	"fmt"
	"net/url"

	pmm "github.com/openeverest/monitoring-pmm/api"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-server-mysql/internal/common"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// pmmIntegration is the integration declared in definition/monitoring.yaml
// and matched by the pmm MonitoringClass.
const pmmIntegration = pmm.IntegrationName

// haproxyMetricsPort is where the operator's HAProxy serves /metrics.
const haproxyMetricsPort = 8404

// MonitoringIntegrations implements controller.MonitoringIntegrationsProvider.
func (p *Provider) MonitoringIntegrations() []string {
	return []string{pmmIntegration}
}

// MonitoringSources implements controller.MonitoringSourcesProvider. It
// publishes HAProxy's own endpoint and, while the engine pods carry the
// mysqld_exporter sidecar, the exporter's, each while a pod of its component
// is ready.
func (p *Provider) MonitoringSources(c *controller.Context) (*corev1alpha1.MonitoringSources, error) {
	sources := &corev1alpha1.MonitoringSources{}
	cluster := &psv1.PerconaServerMySQL{}
	if err := c.Get(cluster, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return sources, nil
		}
		return nil, fmt.Errorf("get PerconaServerMySQL: %w", err)
	}

	if hasMetricsExporter(cluster) && cluster.Status.MySQL.Ready > 0 {
		sources.Metrics = append(sources.Metrics, corev1alpha1.MetricsEndpoint{
			Component: common.ComponentEngine,
			Kind:      "mysql",
			PodSelector: map[string]string{
				controller.InstanceLabel:  c.Name(),
				controller.ComponentLabel: common.ComponentEngine,
			},
			Port:   intstr.FromString(metricsPortName),
			Path:   metricsPath,
			Scheme: "http",
		})
	}
	if cluster.HAProxyEnabled() && cluster.Status.HAProxy.Ready > 0 {
		sources.Metrics = append(sources.Metrics, corev1alpha1.MetricsEndpoint{
			Component: common.ComponentProxy,
			Kind:      "haproxy",
			PodSelector: map[string]string{
				controller.InstanceLabel:  c.Name(),
				controller.ComponentLabel: common.ComponentProxy,
			},
			Port:   intstr.FromInt32(haproxyMetricsPort),
			Path:   metricsPath,
			Scheme: "http",
		})
	}
	return sources, nil
}

// applyMonitoring renders what the Instance's monitoring destinations ask
// for: PMM from its binding, and the exporter sidecar while a class needs
// metrics.
func applyMonitoring(c *controller.Context, cluster *psv1.PerconaServerMySQL, providerSpec *corev1alpha1.ProviderSpec) error {
	live := &psv1.PerconaServerMySQL{}
	if err := c.Get(live, c.Name()); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get PerconaServerMySQL: %w", err)
		}
		live = nil
	}

	required, err := c.RequiredSources()
	if err != nil {
		return fmt.Errorf("resolve required monitoring sources: %w", err)
	}
	if err := applyPMM(c, cluster, live, required.Integrations[pmmIntegration]); err != nil {
		return err
	}
	return applyMetricsExporter(cluster, live, required.Metrics, providerSpec)
}

// applyPMM renders spec.pmm from the Instance's pmm binding, turns PMM off
// once no entry asks for it, and keeps the live spec.pmm while that is
// unresolved (hold rule). A binding whose inputs cannot be rendered is
// reported on the binding and keeps the live spec.pmm too; only API errors
// fail Sync.
func applyPMM(c *controller.Context, cluster, live *psv1.PerconaServerMySQL, required controller.Requirement) error {
	internalSecretName := cluster.InternalSecretName()
	if !required.Else(live != nil && live.Spec.PMM != nil && live.Spec.PMM.Enabled) {
		cluster.Spec.PMM = nil
		return syncPMMServerToken(c, nil, cluster.Spec.SecretsName, internalSecretName)
	}
	if required != controller.Required {
		keepLivePMM(cluster, live)
		return nil
	}

	bindings, err := c.MonitoringBindings()
	if err != nil {
		return fmt.Errorf("list monitoring bindings: %w", err)
	}
	for _, rb := range bindings {
		if !rb.Apply || rb.Integration() != pmmIntegration {
			continue
		}
		spec, token, err := renderPMM(c, cluster, rb)
		if err != nil {
			if errors.As(err, new(apierrors.APIStatus)) && !apierrors.IsNotFound(err) {
				return err
			}
			c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonRenderFailed, err.Error())
			keepLivePMM(cluster, live)
			return nil
		}
		cluster.Spec.PMM = spec
		if err := syncPMMServerToken(c, token, cluster.Spec.SecretsName, internalSecretName); err != nil {
			return err
		}
		c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonAgentTLSNotVerified,
			"pmm-client does not verify the PMM server certificate, whatever spec.tls says: the Percona operator always runs it with PMM_AGENT_SERVER_INSECURE_TLS=1")
		return nil
	}
	keepLivePMM(cluster, live)
	return nil
}

func keepLivePMM(cluster, live *psv1.PerconaServerMySQL) {
	cluster.Spec.PMM = nil
	if live != nil && live.Spec.PMM != nil {
		cluster.Spec.PMM = live.Spec.PMM.DeepCopy()
	}
}

// renderPMM builds spec.pmm for an Apply-able pmm binding and returns the PMM
// server token to mirror. Errors other than API errors describe an input the
// user has to fix. The parameters are decoded with the pmm contract's strict
// decoders, so a parameter this build does not know fails rendering instead
// of being ignored.
func renderPMM(c *controller.Context, cluster *psv1.PerconaServerMySQL, rb controller.ResolvedBinding) (*psv1.PMMSpec, []byte, error) {
	destination, err := pmm.DecodeDestination(rawParameters(rb.Destination.Spec.Parameters))
	if err != nil {
		return nil, nil, fmt.Errorf("MonitoringDestination %s parameters: %w", rb.Destination.Name, err)
	}
	// The decoder accepts only a scheme, a host and a port, which is what
	// the operator's ServerHost takes.
	server, err := url.Parse(destination.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("MonitoringDestination %s parameters.url: %w", rb.Destination.Name, err)
	}

	entry, err := pmm.DecodeInstance(rawParameters(rb.Binding.Spec.Parameters))
	if err != nil {
		return nil, nil, fmt.Errorf("parameters of destination entry %s: %w", rb.Entry.Name, err)
	}

	resources, err := pmmResources(c.Instance().Spec.Components[common.ComponentEngine].Resources, pmmResourceRequirements(entry.Resources))
	if err != nil {
		return nil, nil, fmt.Errorf("parameters of destination entry %s: %w", rb.Entry.Name, err)
	}

	mode, credentials, err := c.MonitoringCredentials(rb)
	if err != nil {
		return nil, nil, err
	}
	token := credentials[pmmTokenKey]
	if len(token) == 0 {
		return nil, nil, fmt.Errorf("authentication mode %s of MonitoringDestination %s provides no %s", mode, rb.Destination.Name, pmmTokenKey)
	}

	return &psv1.PMMSpec{
		Enabled:           true,
		ServerHost:        server.Host,
		Image:             rb.Class.Spec.ProviderManaged.AgentImage,
		CustomClusterName: c.Name(),
		ImagePullPolicy:   corev1.PullIfNotPresent,
		Resources:         resources,
		// The operator only has one pmm-admin params field, applied to the
		// MySQL agent; there is no equivalent for HAProxy in this operator
		// version.
		MySQLParams: pmmAdminParams(c, common.ComponentEngine),
	}, token, nil
}

var (
	_ controller.MonitoringSourcesProvider      = (*Provider)(nil)
	_ controller.MonitoringIntegrationsProvider = (*Provider)(nil)
)
