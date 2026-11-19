package provider

import (
	"fmt"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	psversion "github.com/percona/percona-server-mysql-operator/pkg/version"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-server-mysql/definition/backupclasses"
	"github.com/openeverest/provider-percona-server-mysql/internal/common"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Compile-time check that Provider implements the required interface.
var _ controller.ProviderInterface = (*Provider)(nil)
var _ controller.ReferenceValidator = (*Provider)(nil)

// Provider implements controller.ProviderInterface for the
// provider-percona-server-mysql provider.
type Provider struct {
	controller.BaseProvider
}

// New creates a new Provider instance.
func New() *Provider {
	p := &Provider{}
	p.BaseProvider = controller.BaseProvider{
		ProviderName: common.ProviderName,

		SchemeFuncs: []func(*runtime.Scheme) error{
			psv1.AddToScheme,
			// Sync lists MonitoringBindings and the conformance harness's
			// scheme has only these.
			monitoringv1alpha1.AddToScheme,
			// StatefulSets and Deployments are read to tell whether the
			// cluster has rolled out.
			appsv1.AddToScheme,
		},

		WatchConfigs: []controller.WatchConfig{
			controller.WatchOwned(
				&psv1.PerconaServerMySQL{},
			),
			// Watch Restores so the Instance stops reporting a restore as
			// soon as one reaches a terminal state.
			controller.WatchExternal(&backupv1alpha1.Restore{},
				handler.EnqueueRequestsFromMapFunc(enqueueRestoreInstance()),
			),
			// Watch the operator's restores, including those created on it
			// directly: a failed one leaves the cluster paused without
			// touching the PerconaServerMySQL object again.
			controller.WatchExternal(&psv1.PerconaServerMySQLRestore{},
				handler.EnqueueRequestsFromMapFunc(enqueueOperatorRestoreInstance()),
			),
			// Watch the cluster's StatefulSets and the MySQL Router
			// Deployment: the Instance has converged only once they have
			// rolled out, which the operator's own status can report too
			// early. They are owned by the PerconaServerMySQL, so map them by
			// their controller.
			controller.WatchExternal(&appsv1.StatefulSet{},
				handler.EnqueueRequestsFromMapFunc(enqueueClusterWorkloadInstance()),
			),
			controller.WatchExternal(&appsv1.Deployment{},
				handler.EnqueueRequestsFromMapFunc(enqueueClusterWorkloadInstance()),
			),
		},
	}
	p.SetShippedBackupClasses(backupclasses.Shipped())
	return p
}

// Sync ensures all required resources exist and are configured correctly.
//
// This is the main reconciliation logic. Create or update your
// operator's custom resource(s) based on the Instance spec.
func (p *Provider) Sync(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing instance", "name", c.Name())

	// The PS operator toggles cluster.Spec.Pause directly while a
	// PerconaServerMySQLRestore runs (see the psrestore controller). Sync
	// must not fight that by re-asserting the desired spec (which defaults
	// Pause back to false) while a restore is in flight.
	activeRestore, err := hasActiveRestoreForInstance(c, c.Namespace(), c.Name())
	if err != nil {
		return fmt.Errorf("check active restores for %q: %w", c.Name(), err)
	}
	if activeRestore {
		l.Info("Skipping spec sync while restore is active", "name", c.Name())
		return nil
	}

	providerSpec, err := c.ProviderSpec()
	if err != nil {
		return fmt.Errorf("get provider spec: %w", err)
	}

	bundleComponents := map[string]string{}
	bundleName := controller.EffectiveVersionBundleName(providerSpec, c.Instance())

	if bundleName != "" {
		bundle, err := controller.ResolveVersionBundle(providerSpec, bundleName)
		if err != nil {
			return fmt.Errorf("resolve version bundle %q: %w", bundleName, err)
		}

		bundleComponents = bundle.Components
	}

	engine, ok := c.Instance().Spec.Components[common.ComponentEngine]
	if !ok {
		return fmt.Errorf("instance spec missing %q component", common.ComponentEngine)
	}

	cluster := &psv1.PerconaServerMySQL{
		ObjectMeta: c.ObjectMeta(c.Name()),
		Spec: psv1.PerconaServerMySQLSpec{
			CRVersion: psversion.Version(),

			SecretsName:    c.Name() + "-secrets",
			UpdateStrategy: appsv1.RollingUpdateStatefulSetStrategyType,
		},
	}

	tls, err := tlsParameters(c)
	if err != nil {
		return err
	}
	configureTLS(c, cluster, tls)

	// Configure the MySQL engine.
	engineVersion := engine.Version
	if engineVersion == "" {
		engineVersion = bundleComponents[common.ComponentEngine]
	}

	if engine.Image != "" {
		cluster.Spec.MySQL.Image = engine.Image
	} else if engineVersion != "" {
		cluster.Spec.MySQL.Image = controller.GetImageForVersion(
			providerSpec,
			common.ComponentEngine,
			engineVersion,
		)
	}

	if cluster.Spec.MySQL.Image == "" {
		cluster.Spec.MySQL.Image = controller.GetDefaultImage(
			providerSpec,
			"mysql",
		)
	}

	if cluster.Spec.MySQL.Image == "" {
		return fmt.Errorf(
			"cannot resolve MySQL image: set engine.image or engine.version",
		)
	}

	size, ok := engineReplicas(c.Instance())
	if !ok {
		return fmt.Errorf(
			"instance spec missing %q component replicas",
			common.ComponentEngine,
		)
	}

	cluster.Spec.MySQL.Size = size

	if engine.Resources != nil {
		cluster.Spec.MySQL.Resources = *engine.Resources
	}

	if engine.Storage != nil {
		cluster.Spec.MySQL.VolumeSpec = &psv1.VolumeSpec{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: engine.Storage.Size,
					},
				},
			},
		}

		if engine.Storage.StorageClass != nil {
			cluster.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.
				StorageClassName = engine.Storage.StorageClass
		}
	}

	applySchedulingPolicy(&cluster.Spec.MySQL.PodSpec, engine.SchedulingPolicy)

	if err := applyEngineConfiguration(cluster, engine); err != nil {
		return fmt.Errorf("apply engine configuration: %w", err)
	}

	applyEngineExpose(cluster, engine)

	topologyType := effectiveTopologyType(c.Instance())

	switch topologyType {
	case common.TopologyAsync:
		cluster.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
	case common.TopologyGroupReplication:
		cluster.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
	default:
		return fmt.Errorf("instance spec has unsupported topology type %q", topologyType)
	}

	if mysqlSizeRequiresUnsafe(topologyType, cluster.Spec.MySQL.Size) {
		cluster.Spec.Unsafe.MySQLSize = true
	}

	cluster.Spec.MySQL.AutoRecovery = true

	if err := applyOrchestrator(cluster, c.Instance(), providerSpec); err != nil {
		return fmt.Errorf("apply orchestrator: %w", err)
	}

	if err := applyProxy(cluster, c.Instance(), providerSpec); err != nil {
		return fmt.Errorf("apply proxy: %w", err)
	}

	if err := applyMonitoring(c, cluster, providerSpec); err != nil {
		return fmt.Errorf("apply monitoring: %w", err)
	}

	if err := applyBackupSettings(c, cluster); err != nil {
		return fmt.Errorf("apply backup settings: %w", err)
	}

	if err := c.Apply(cluster); err != nil {
		return fmt.Errorf("apply PerconaServerMySQL %q: %w", c.Name(), err)
	}

	l.Info("PerconaServerMySQL cluster synced", "cluster", c.Name())

	return nil
}

// Status reports what the provider observes about the cluster.
func (p *Provider) Status(c *controller.Context) (controller.Status, error) {
	return clusterStatus(c)
}

func connectionDetails(
	c *controller.Context,
	cluster *psv1.PerconaServerMySQL,
) (controller.ConnectionDetails, error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, cluster.Spec.SecretsName); err != nil {
		return controller.ConnectionDetails{}, fmt.Errorf(
			"get secret %q: %w", cluster.Spec.SecretsName, err,
		)
	}

	password, ok := secret.Data[string(psv1.UserRoot)]
	if !ok {
		return controller.ConnectionDetails{}, fmt.Errorf(
			"secret %q missing %q key", cluster.Spec.SecretsName, psv1.UserRoot,
		)
	}

	details := controller.ConnectionDetails{
		Type:     "mysql",
		Provider: common.ProviderName,
		Host:     cluster.Status.Host,
		Port:     "3306",
		Username: string(psv1.UserRoot),
		Password: string(password),
	}

	// Clients verify the instance's certificate against this CA
	// (the operator's own CA is not publicly trusted).
	ca, err := tlsConnectionCA(c, cluster)
	if err != nil {
		return controller.ConnectionDetails{}, err
	}
	if len(ca) > 0 {
		details.AdditionalProperties = map[string]string{caKey: string(ca)}
	}

	return details, nil
}

// Cleanup handles deletion of provider-managed resources.
//
// Called when the Instance has a deletion timestamp set.
// Delete any resources that are not automatically cleaned up
// via owner references.
func (p *Provider) Cleanup(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up instance", "name", c.Name())

	cluster := &psv1.PerconaServerMySQL{}

	if err := c.Get(cluster, c.Name()); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("get PerconaServerMySQL %q for cleanup: %w", c.Name(), err)
	}

	if !cluster.GetDeletionTimestamp().IsZero() {
		return controller.WaitFor("waiting for PerconaServerMySQL to be deleted")
	}

	if err := c.Delete(cluster); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("delete PerconaServerMySQL %q: %w", c.Name(), err)
	}

	return controller.WaitFor("waiting for PerconaServerMySQL to be deleted")
}
