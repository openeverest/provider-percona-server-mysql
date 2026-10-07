package provider

import (
	"context"
	"fmt"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	psversion "github.com/percona/percona-server-mysql-operator/pkg/version"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-server-mysql/internal/common"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Compile-time check that Provider implements the required interface.
var _ controller.ProviderInterface = (*Provider)(nil)
var _ controller.FieldIndexProvider = (*Provider)(nil)

// Provider implements controller.ProviderInterface for the
// provider-percona-server-mysql provider.
type Provider struct {
	controller.BaseProvider
	client client.Client
}

func (p *Provider) SetClient(c client.Client) {
	p.client = c
}

// New creates a new Provider instance.
func New() *Provider {
	p := &Provider{}
	p.BaseProvider = controller.BaseProvider{
		ProviderName: common.ProviderName,

		SchemeFuncs: []func(*runtime.Scheme) error{
			psv1.AddToScheme,
			monitoringv1alpha1.AddToScheme,
		},

		WatchConfigs: []controller.WatchConfig{
			controller.WatchOwned(
				&psv1.PerconaServerMySQL{},
			),
			controller.WatchExternal(
				&monitoringv1alpha1.MonitoringConfig{},
				handler.EnqueueRequestsFromMapFunc(p.enqueueMonitoringConfig),
				controller.ResourceVersionChangedPredicate,
			),
			controller.WatchExternal(&backupv1alpha1.Restore{},
				handler.EnqueueRequestsFromMapFunc(enqueueRestoreInstance()),
			),
		},
	}
	return p
}

// Validate checks if the Instance spec is valid.
//
// Add your provider-specific validation logic here.
// Return an error if the spec is invalid.
func (p *Provider) Validate(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating instance", "name", c.Name())

	if err := validateOrchestrator(c.Instance()); err != nil {
		return err
	}
	if err := validateProxy(c.Instance()); err != nil {
		return err
	}

	return nil
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
			SSLSecretName:  c.Name() + "-ssl",
			UpdateStrategy: appsv1.RollingUpdateStatefulSetStrategyType,
		},
	}

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

	if engine.Replicas == nil {
		return fmt.Errorf(
			"instance spec missing %q component replicas",
			common.ComponentEngine,
		)
	}

	cluster.Spec.MySQL.Size = *engine.Replicas

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

	if err := applyMonitoringSettings(c, cluster, providerSpec); err != nil {
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

// Status returns the current status of the PerconaServerMySQL resource.
func (p *Provider) Status(
	c *controller.Context,
) (controller.Status, error) {
	l := log.FromContext(c.Context())
	l.Info("Computing status", "name", c.Name())

	cluster := &psv1.PerconaServerMySQL{}

	if err := c.Get(cluster, c.Name()); err != nil {
		if errors.IsNotFound(err) {
			return controller.Provisioning("waiting for PerconaServerMySQL resource"), nil
		}

		return controller.Status{}, fmt.Errorf("get PerconaServerMySQL %q: %w", c.Name(), err)
	}

	// A restore drives the engine through paused, starting and momentarily
	// ready states, so reading the phase off the engine alone makes the
	// Instance flap between Restoring, Provisioning and Ready while one is
	// in flight. The Restore CR reaching a terminal state is what ends the
	// restore, so let it own the phase for as long as it is running.
	activeRestore, err := hasActiveRestoreForInstance(c, c.Namespace(), c.Name())
	if err != nil {
		return controller.Status{}, fmt.Errorf("check active restores for %q: %w", c.Name(), err)
	}
	if activeRestore {
		return controller.Restoring("Restore is running"), nil
	}

	switch cluster.Status.State {
	case psv1.StateReady:
		details, err := connectionDetails(c, cluster)
		if err != nil {
			return controller.Status{}, fmt.Errorf(
				"get connection details for PerconaServerMySQL %q: %w", c.Name(), err,
			)
		}

		return controller.ReadyWithConnectionDetails(details), nil
	case psv1.StateError:
		return controller.Failed("PerconaServerMySQL cluster is in error state"), nil
	case psv1.StatePaused, psv1.StateStopping:
		return controller.Provisioning("PerconaServerMySQL cluster is paused"), nil
	case psv1.StateInitializing:
		return controller.Initializing("PerconaServerMySQL cluster is initializing"), nil
	default:
		return controller.Provisioning("waiting for PerconaServerMySQL to become ready"), nil
	}
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

	return controller.ConnectionDetails{
		Type:     "mysql",
		Provider: common.ProviderName,
		Host:     cluster.Status.Host,
		Port:     "3306",
		Username: string(psv1.UserRoot),
		Password: string(password),
	}, nil
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

func (p *Provider) enqueueMonitoringConfig(ctx context.Context, obj client.Object) []reconcile.Request {
	if p.client == nil {
		return nil
	}

	mc, ok := obj.(*monitoringv1alpha1.MonitoringConfig)
	if !ok {
		return nil
	}

	instances := &corev1alpha1.InstanceList{}
	if err := p.client.List(ctx, instances,
		client.InNamespace(mc.Namespace),
		client.MatchingFields{monitoringConfigRefFieldPath: mc.Name},
	); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(instances.Items))
	for i := range instances.Items {
		instance := instances.Items[i]
		if instance.Spec.ProviderRef.Name != p.Name() {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&instance)})
	}
	return requests
}

func (p *Provider) FieldIndexes() []controller.FieldIndex {
	return []controller.FieldIndex{
		{
			Object:    &corev1alpha1.Instance{},
			FieldPath: monitoringConfigRefFieldPath,
			Extractor: func(obj client.Object) []string {
				instance, ok := obj.(*corev1alpha1.Instance)
				if !ok {
					return nil
				}

				monitoringComponent, ok := instance.Spec.Components[common.ComponentMonitoring]
				if !ok {
					return nil
				}

				monitoringConfigName, err := monitoringConfigNameFromComponent(monitoringComponent)
				if err != nil || monitoringConfigName == "" {
					return nil
				}

				return []string{monitoringConfigName}
			},
		},
	}
}
