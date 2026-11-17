package provider

import (
	"fmt"
	"slices"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-server-mysql/internal/common"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// monitorUser is the operator-managed MySQL user with the read-only
	// grants exporters need.
	monitorUser = string(psv1.UserMonitor)

	mysqldExporterContainerName = "mysqld-exporter"
	mysqldExporterPort          = 9104
	metricsPortName             = "metrics"
	metricsPath                 = "/metrics"
)

// applyMetricsExporter adds a mysqld_exporter sidecar to every engine pod
// while a class bound to the Instance requires metrics. While that is
// unresolved the sidecar stays as live, so the pods do not roll twice.
func applyMetricsExporter(cluster, live *psv1.PerconaServerMySQL, required controller.Requirement, providerSpec *corev1alpha1.ProviderSpec) error {
	if !required.Else(hasMetricsExporter(live)) {
		return nil
	}

	image := defaultImageForComponentType(providerSpec, common.ComponentTypeMySQLDExporter)
	if image == "" {
		return fmt.Errorf("cannot resolve image for component type %q", common.ComponentTypeMySQLDExporter)
	}

	cluster.Spec.MySQL.Sidecars = append(cluster.Spec.MySQL.Sidecars, mysqldExporterContainer(image, cluster.Spec.SecretsName))
	return nil
}

func mysqldExporterContainer(image, usersSecretName string) corev1.Container {
	return corev1.Container{
		Name:            mysqldExporterContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"--mysqld.address=127.0.0.1:3306",
			"--mysqld.username=" + monitorUser,
		},
		Env: []corev1.EnvVar{{
			Name: "MYSQLD_EXPORTER_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: usersSecretName},
					Key:                  monitorUser,
				},
			},
		}},
		Ports: []corev1.ContainerPort{{
			Name:          metricsPortName,
			ContainerPort: mysqldExporterPort,
			Protocol:      corev1.ProtocolTCP,
		}},
	}
}

// hasMetricsExporter reports whether the engine CR, nil before it exists,
// carries the exporter sidecar.
func hasMetricsExporter(cluster *psv1.PerconaServerMySQL) bool {
	if cluster == nil {
		return false
	}
	return slices.ContainsFunc(cluster.Spec.MySQL.Sidecars, func(c corev1.Container) bool {
		return c.Name == mysqldExporterContainerName
	})
}

func defaultImageForComponentType(spec *corev1alpha1.ProviderSpec, componentType string) string {
	if image := controller.GetDefaultImage(spec, componentType); image != "" {
		return image
	}
	ct, ok := spec.ComponentTypes[componentType]
	if !ok {
		return ""
	}
	for _, v := range ct.Versions {
		if v.Image != "" {
			return v.Image
		}
	}
	return ""
}
