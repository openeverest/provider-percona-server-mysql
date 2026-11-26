package provider

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	pmm "github.com/openeverest/monitoring-pmm/api"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const (
	// pmmTokenKey is the key of the PMM server token in the destination's
	// credentials Secret.
	pmmTokenKey = pmm.KeyToken
	// pmmServerTokenKey is where the operator reads the PMM 3 server token
	// in the users and internal Secrets.
	pmmServerTokenKey = "pmmservertoken"
)

// PMM client requests per engine size, from
// https://github.com/Tusamarco/mysqloperatorcalculator.
var (
	pmmRequestsSmall = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("95m"),
		corev1.ResourceMemory: resource.MustParse("99604Ki"),
	}
	pmmRequestsMedium = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("228m"),
		corev1.ResourceMemory: resource.MustParse("199168Ki"),
	}
	pmmRequestsLarge = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("228m"),
		corev1.ResourceMemory: resource.MustParse("796907Ki"),
	}
)

var (
	mysqlMemMedium = resource.MustParse("8Gi")
	mysqlMemLarge  = resource.MustParse("32Gi")
)

// memoryLimitOrRequest returns the engine's memory limit, or its request when
// no limit is set, the zero Quantity when neither is set.
func memoryLimitOrRequest(resources *corev1.ResourceRequirements) resource.Quantity {
	if resources == nil {
		return resource.Quantity{}
	}
	if q, ok := resources.Limits[corev1.ResourceMemory]; ok && !q.IsZero() {
		return q
	}
	if q, ok := resources.Requests[corev1.ResourceMemory]; ok {
		return q
	}
	return resource.Quantity{}
}

// pmmResources returns the PMM client resources: requests sized from the
// engine's memory, overridden by whatever the entry sets. A sized request
// above a limit the entry sets is lowered to that limit.
func pmmResources(engine, entry *corev1.ResourceRequirements) (corev1.ResourceRequirements, error) {
	requests := pmmRequestsSmall
	switch mem := memoryLimitOrRequest(engine); {
	case mem.Cmp(mysqlMemLarge) >= 0:
		requests = pmmRequestsLarge
	case mem.Cmp(mysqlMemMedium) >= 0:
		requests = pmmRequestsMedium
	}

	out := corev1.ResourceRequirements{Requests: maps.Clone(requests)}
	if entry == nil {
		return out, nil
	}
	for _, name := range slices.Sorted(maps.Keys(entry.Limits)) {
		limit := entry.Limits[name]
		if request, set := entry.Requests[name]; set {
			if request.Cmp(limit) > 0 {
				return corev1.ResourceRequirements{}, fmt.Errorf("resources.requests.%s (%s) is above resources.limits.%s (%s)",
					name, request.String(), name, limit.String())
			}
			continue
		}
		if sized, ok := out.Requests[name]; ok && sized.Cmp(limit) > 0 {
			out.Requests[name] = limit
		}
	}
	maps.Copy(out.Requests, entry.Requests)
	out.Limits = maps.Clone(entry.Limits)
	return out, nil
}

// pmmAdminParams returns the pmm-admin flags that label the instance's
// metrics in PMM, so consumers can scope queries to one instance. Label
// names follow the Prometheus form of the pod labels
// (core_openeverest_io_instance), and namespace matches the label on
// Kubernetes metrics in PMM. The operator runs pmm-admin through a shell;
// Kubernetes names need no quoting.
func pmmAdminParams(c *controller.Context, component string) string {
	labels := []string{
		"namespace=" + c.Namespace(),
		prometheusLabelName(controller.InstanceLabel) + "=" + c.Name(),
	}
	if component != "" {
		labels = append(labels, prometheusLabelName(controller.ComponentLabel)+"="+component)
	}
	return "--custom-labels=" + strings.Join(labels, ",")
}

// pmmMySQLParams are the pmm-admin flags for the MySQL agent. When the
// engine requires TLS, the agent connects with TLS too, without verifying the
// certificate: it only connects to its own pod.
func pmmMySQLParams(c *controller.Context, cluster *psv1.PerconaServerMySQL) string {
	params := pmmAdminParams(c, common.ComponentEngine)
	if requiresSecureTransport(cluster) {
		params += " --tls --tls-skip-verify"
	}
	return params
}

var invalidPrometheusLabelChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// prometheusLabelName converts a Kubernetes label key to a valid Prometheus
// label name the way Prometheus does for Kubernetes service discovery.
func prometheusLabelName(key string) string {
	return invalidPrometheusLabelChars.ReplaceAllString(key, "_")
}

// pmmResourceRequirements converts the resources of a pmm entry's parameters
// into the container's; nil sets none.
func pmmResourceRequirements(r *pmm.Resources) *corev1.ResourceRequirements {
	if r == nil {
		return nil
	}
	return &corev1.ResourceRequirements{Limits: resourceList(r.Limits), Requests: resourceList(r.Requests)}
}

func resourceList(quantities map[string]resource.Quantity) corev1.ResourceList {
	if quantities == nil {
		return nil
	}
	list := make(corev1.ResourceList, len(quantities))
	for name, q := range quantities {
		list[corev1.ResourceName(name)] = q
	}
	return list
}

// rawParameters returns the JSON of a parameters field, nil when unset.
func rawParameters(parameters *runtime.RawExtension) []byte {
	if parameters == nil {
		return nil
	}
	return parameters.Raw
}

// syncPMMServerToken mirrors token into usersSecretName and
// internalSecretName, where the operator reads it, or removes the mirror
// when token is nil. Unlike PXC, the PS operator compares the token in both
// the users Secret and internal-<name> before enabling PMM (see
// PMMSpec.HasSecret and PerconaServerMySQL.InternalSecretName), so both must
// be kept in sync or the operator treats a password rotation as pending and
// defers to Orchestrator. The operator creates these Secrets; until they
// exist there is nothing to update.
func syncPMMServerToken(c *controller.Context, token []byte, secretNames ...string) error {
	for _, name := range secretNames {
		if err := syncPMMServerTokenSecret(c, name, token); err != nil {
			return err
		}
	}
	return nil
}

func syncPMMServerTokenSecret(c *controller.Context, usersSecretName string, token []byte) error {
	secret := &corev1.Secret{}
	if err := c.Get(secret, usersSecretName); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("get Secret %q: %w", usersSecretName, err)
	}

	current, mirrored := secret.Data[pmmServerTokenKey]
	if (token == nil && !mirrored) || (token != nil && bytes.Equal(current, token)) {
		return nil
	}

	patch := client.MergeFrom(secret.DeepCopy())
	if token == nil {
		delete(secret.Data, pmmServerTokenKey)
	} else {
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[pmmServerTokenKey] = token
	}
	if err := c.Client().Patch(c.Context(), secret, patch); err != nil {
		return fmt.Errorf("sync PMM server token to Secret %q: %w", usersSecretName, err)
	}
	return nil
}
