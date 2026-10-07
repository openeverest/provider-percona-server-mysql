package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/provider-percona-server-mysql/definition/components"
	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const (
	monitoringConfigRefFieldPath = "spec.components.monitoring.parameters.monitoringConfigName"
	monitoringConfigAPIKeyKey    = "apiKey"
	// psPMMServerToken is the users-secret key the PS operator reads for the
	// PMM client (PMM_AGENT_SERVER_PASSWORD). Unlike PXC, this operator always
	// uses the token key, including for PMM 2 client images.
	psPMMServerToken = string(psv1.UserPMMServerToken)
)

func applyMonitoringSettings(c *controller.Context, cluster *psv1.PerconaServerMySQL, providerSpec *corev1alpha1.ProviderSpec) error {
	monitoringComponent, ok := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !ok {
		cluster.Spec.PMM = nil
		return nil
	}
	monitoringType := monitoringComponent.Type
	if monitoringType == "" {
		monitoringType = common.MonitoringTypePMM
	}

	if monitoringType != common.MonitoringTypePMM {
		return fmt.Errorf("unsupported monitoring component type %q", monitoringComponent.Type)
	}

	monitoringConfigName, err := monitoringConfigNameFromComponent(monitoringComponent)
	if err != nil {
		return err
	}
	if monitoringConfigName == "" {
		cluster.Spec.PMM = nil
		return nil
	}

	monitoringCfg := &monitoringv1alpha1.MonitoringConfig{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: c.Namespace(), Name: monitoringConfigName}, monitoringCfg); err != nil {
		return fmt.Errorf("get MonitoringConfig %q: %w", monitoringConfigName, err)
	}
	if monitoringCfg.Spec.Type != monitoringv1alpha1.PMMMonitoringType || monitoringCfg.Spec.PMM == nil {
		return fmt.Errorf("MonitoringConfig %q must be type %q", monitoringConfigName, monitoringv1alpha1.PMMMonitoringType)
	}
	if monitoringCfg.Spec.PMM.CredentialsSecretRef.Name == "" {
		return fmt.Errorf("MonitoringConfig %q must set spec.pmm.credentialsSecretRef.name", monitoringConfigName)
	}

	serverHost, err := pmmServerHostFromURL(monitoringCfg.Spec.PMM.URL)
	if err != nil {
		return fmt.Errorf("resolve PMM server host from MonitoringConfig %q URL: %w", monitoringConfigName, err)
	}

	pmmImage := monitoringImageForComponent(c, providerSpec, monitoringType, monitoringComponent)
	if pmmImage == "" {
		return fmt.Errorf("cannot resolve PMM image for component %q", common.ComponentMonitoring)
	}

	secretsName := cluster.Spec.SecretsName
	if secretsName == "" {
		secretsName = c.Name() + "-secrets"
	}
	// The operator enables PMM only when pmmservertoken matches in the users
	// secret and internal-<name>. A mismatch is treated as a password change
	// and, on async, resolved via Orchestrator. This topology may not run
	// Orchestrator, so keep both secrets in sync here.
	if err := syncPMMCredentials(c, monitoringCfg.Spec.PMM.CredentialsSecretRef.Name, secretsName, cluster.InternalSecretName()); err != nil {
		return err
	}

	cluster.Spec.PMM = &psv1.PMMSpec{
		Enabled:           true,
		ServerHost:        serverHost,
		Image:             pmmImage,
		CustomClusterName: c.Name(),
		ImagePullPolicy:   corev1.PullIfNotPresent,
	}
	if monitoringComponent.Resources != nil {
		cluster.Spec.PMM.Resources = *monitoringComponent.Resources
	}
	return nil
}

func monitoringConfigNameFromComponent(component corev1alpha1.ComponentSpec) (string, error) {
	if component.Parameters == nil || len(component.Parameters.Raw) == 0 {
		return "", nil
	}

	cfg := &components.PMMParameters{}
	if err := json.Unmarshal(component.Parameters.Raw, cfg); err != nil {
		return "", fmt.Errorf("decode monitoring component parameters: %w", err)
	}
	if cfg.MonitoringConfigName == nil {
		return "", nil
	}

	return *cfg.MonitoringConfigName, nil
}

func pmmServerHostFromURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("url is empty")
	}
	if !strings.Contains(rawURL, "://") {
		return strings.TrimSuffix(rawURL, "/"), nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	return u.Host, nil
}

func syncPMMCredentials(c *controller.Context, credentialsSecretName string, secretNames ...string) error {
	credentialsSecret := &corev1.Secret{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: c.Namespace(), Name: credentialsSecretName}, credentialsSecret); err != nil {
		return fmt.Errorf("get PMM credentials Secret %q: %w", credentialsSecretName, err)
	}
	apiKey, ok := credentialsSecret.Data[monitoringConfigAPIKeyKey]
	if !ok || len(apiKey) == 0 {
		return fmt.Errorf("PMM credentials Secret %q must contain non-empty %q key", credentialsSecretName, monitoringConfigAPIKeyKey)
	}

	for _, secretName := range secretNames {
		if err := ensurePMMToken(c, secretName, apiKey); err != nil {
			return err
		}
	}
	return nil
}

func ensurePMMToken(c *controller.Context, secretName string, apiKey []byte) error {
	secret := &corev1.Secret{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: c.Namespace(), Name: secretName}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			// The operator creates these secrets; retry on the next reconcile.
			return nil
		}
		return fmt.Errorf("get PMM users Secret %q: %w", secretName, err)
	}

	if secret.Data != nil && bytes.Equal(secret.Data[psPMMServerToken], apiKey) {
		return nil
	}

	orig := secret.DeepCopy()
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[psPMMServerToken] = append([]byte(nil), apiKey...)

	if err := c.Client().Patch(c.Context(), secret, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("sync PMM credentials to Secret %q: %w", secretName, err)
	}

	return nil
}

func monitoringImageForComponent(c *controller.Context, providerSpec *corev1alpha1.ProviderSpec, monitoringType string, component corev1alpha1.ComponentSpec) string {
	if component.Image != "" {
		return component.Image
	}

	version := component.Version
	if version == "" {
		selectedBundle := controller.EffectiveVersionBundleName(providerSpec, c.Instance())
		if selectedBundle != "" {
			if bundle, err := controller.ResolveVersionBundle(providerSpec, selectedBundle); err == nil {
				version = bundle.Components[common.ComponentMonitoring]
			}
		}
	}

	return imageForComponentType(providerSpec, monitoringType, version, "")
}
