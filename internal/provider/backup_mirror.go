package provider

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	s3class "github.com/openeverest/openeverest/v2/backupstorageclasses/s3"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/provider-percona-server-mysql/definition/backupclasses"
	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

// Compile-time interface checks.
var _ controller.BackupMirror = (*Provider)(nil)

// backupTypeCron is the value the PS operator stamps on naming.LabelBackupType
// for backups it created from a schedule (see reconcileScheduledBackup /
// createBackupJobFunc in pkg/controller/ps/backup.go). On-demand backups
// created by SyncBackup carry neither this label nor naming.LabelBackupAncestor.
const backupTypeCron = "cron"

// backupClassName is the BackupClass the provider ships, defined in
// definition/backupclasses.
const backupClassName = "ps"

// Mirror implements controller.BackupMirror (optional). The runtime invokes
// Mirror() for operator backup events. Return a Backup CR to create it
// idempotently, or nil to skip (on-demand backups, missing Instance, or backups
// when Instance has no backup configuration).
func (p *Provider) Mirror(ctx context.Context, c client.Client, obj client.Object) (*backupv1alpha1.Backup, error) {
	opBackup, ok := obj.(*psv1.PerconaServerMySQLBackup)
	if !ok {
		return nil, fmt.Errorf("unexpected operator backup type %T", obj)
	}

	if !opBackup.DeletionTimestamp.IsZero() {
		return nil, nil
	}

	scheduleName, isScheduled := scheduledBackupName(opBackup)
	if !isScheduled {
		return nil, nil
	}

	// Skip backups that are already owned by an OpenEverest Backup CR
	// (on-demand backups created by SyncBackup).
	for _, owner := range opBackup.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == backupv1alpha1.GroupVersion.String() && owner.Kind == "Backup" {
			return nil, nil
		}
	}

	instance := &corev1alpha1.Instance{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: opBackup.Namespace, Name: opBackup.Spec.ClusterName}, instance); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get instance %q: %w", opBackup.Spec.ClusterName, err)
	}

	// Skip mirroring when the Instance is being deleted. During cascade
	// deletion the PerconaServerMySQL cluster may still be running (and
	// scheduled backups may still fire) but we must not create new Backup
	// CRs that would delay the Instance cleanup.
	if !instance.GetDeletionTimestamp().IsZero() {
		return nil, nil
	}

	if instance.Spec.ProviderRef.Name != common.ProviderName || instance.Spec.Backup == nil || instance.Spec.Backup.ClassRef.Name == "" {
		return nil, nil
	}

	if opBackup.Spec.StorageName == "" {
		return nil, nil
	}

	return &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opBackup.Name,
			Namespace: opBackup.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: psv1.GroupVersion.String(),
				Kind:       "PerconaServerMySQLBackup",
				Name:       opBackup.Name,
				UID:        opBackup.UID,
			}},
		},
		Spec: backupv1alpha1.BackupSpec{
			Origin: backupv1alpha1.BackupOrigin{
				Type:        backupv1alpha1.BackupOriginTypeInstance,
				InstanceRef: &apicommon.ObjectRef{Name: opBackup.Spec.ClusterName},
			},
			ClassRef:     apicommon.ObjectRef{Name: instance.Spec.Backup.ClassRef.Name},
			StorageRef:   apicommon.ObjectRef{Name: opBackup.Spec.StorageName},
			ScheduleName: scheduleName,
		},
	}, nil
}

// scheduledBackupName determines whether opBackup was produced by the
// cluster's cron scheduler (rather than an on-demand SyncBackup call) and, if
// so, returns the InstanceBackupSchedule name that produced it.
//
// The operator labels every scheduled backup with naming.LabelBackupType="cron"
// and naming.LabelBackupAncestor="<clusterPrefix>-<scheduleName>" (see
// reconcileScheduledBackup / createBackupJobFunc in
// pkg/controller/ps/backup.go). On-demand backups created via SyncBackup have
// neither label set.
//
// PerconaServerMySQLBackup defines its own Labels(name, component) method
// (used by the operator to build labels for child resources), which shadows
// the embedded metav1.ObjectMeta.Labels field on the struct's own labels —
// hence the explicit .ObjectMeta.Labels access below.
func scheduledBackupName(opBackup *psv1.PerconaServerMySQLBackup) (string, bool) {
	if opBackup.ObjectMeta.Labels[naming.LabelBackupType] != backupTypeCron {
		return "", false
	}
	ancestor := opBackup.ObjectMeta.Labels[naming.LabelBackupAncestor]
	if ancestor == "" {
		return opBackup.Name, true
	}
	prefix := backupJobClusterPrefix(opBackup.Namespace+"-"+opBackup.Spec.ClusterName) + "-"
	if strings.HasPrefix(ancestor, prefix) {
		return strings.TrimPrefix(ancestor, prefix), true
	}
	return ancestor, true
}

// backupJobClusterPrefix mirrors the PS operator's internal
// backupJobClusterPrefix (pkg/controller/ps/backup.go), which derives the
// schedule-job name prefix from a SHA1 hash of "<namespace>-<clusterName>".
func backupJobClusterPrefix(clusterName string) string {
	h := sha1.Sum([]byte(clusterName))
	return hex.EncodeToString(h[:])[:5]
}

// applyBackupSettings translates Instance.Spec.Backup into cluster.Spec.Backup,
// registering one PerconaServerMySQLBackup storage per InstanceBackupStorage
// the runtime clears to render and one BackupSchedule per enabled
// InstanceBackupSchedule on a storage the engine has.
func applyBackupSettings(c *controller.Context, cluster *psv1.PerconaServerMySQL) error {
	if c.Instance().Spec.Backup == nil || !c.Instance().Spec.Backup.Enabled {
		cluster.Spec.Backup = nil
		return nil
	}

	backupClass, err := c.BackupClassForInstance()
	if err != nil {
		return &controller.BackupConfigError{Reason: "BackupClassLookupFailed", Message: err.Error()}
	}
	if err := controller.ValidateInstanceBackupAgainstClass(c.Instance(), backupClass); err != nil {
		reason := "InvalidBackupConfiguration"
		if errors.Is(err, controller.ErrBackupClassLimitsExceeded) {
			reason = controller.LimitsExceededReason
		}
		return &controller.BackupConfigError{Reason: reason, Message: err.Error()}
	}

	if len(c.Instance().Spec.Backup.Storages) == 0 {
		return &controller.BackupConfigError{Reason: "NoStoragesConfigured", Message: "spec.backup.enabled=true requires at least one storage"}
	}

	providerSpec, err := c.ProviderSpec()
	if err != nil {
		return err
	}

	backupSpec := &psv1.BackupSpec{
		Enabled:  true,
		Storages: make(map[string]*psv1.BackupStorageSpec, len(c.Instance().Spec.Backup.Storages)),
	}

	backupSpec.Image = resolveBackupImage(c, providerSpec)
	if backupSpec.Image == "" {
		return &controller.BackupConfigError{Reason: "BackupImageUnavailable", Message: "cannot resolve xtrabackup image; set componentTypes.backup in the provider's version catalog"}
	}

	liveBackup := &psv1.BackupSpec{}
	live := &psv1.PerconaServerMySQL{}
	if err := c.Get(live, c.Name()); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get PerconaServerMySQL: %w", err)
		}
	} else if live.Spec.Backup != nil {
		liveBackup = live.Spec.Backup
	}

	storages, err := c.BackupStorages()
	if err != nil {
		return fmt.Errorf("resolve backup storages: %w", err)
	}
	for _, s := range storages {
		if s.Apply() {
			opStorage, err := renderBackupStorage(c, s)
			if err == nil {
				backupSpec.Storages[s.Name()] = opStorage
				continue
			}
			if errors.As(err, new(apierrors.APIStatus)) && !apierrors.IsNotFound(err) {
				return err
			}
			c.RejectBackupStorage(s.Name(), err)
		}
		// Hold rule: keep the storage the engine already has, never add one.
		if held, ok := liveBackup.Storages[s.Name()]; ok {
			backupSpec.Storages[s.Name()] = held.DeepCopy()
		}
	}

	for _, storage := range c.Instance().Spec.Backup.Storages {
		if _, ok := backupSpec.Storages[storage.StorageRef.Name]; !ok {
			continue
		}
		for _, schedule := range storage.Schedules {
			if !schedule.Enabled {
				continue
			}
			backupType, err := resolveBackupType(schedule.Parameters)
			if err != nil {
				return &controller.BackupConfigError{Reason: "InvalidBackupParameters", Message: fmt.Sprintf("storage %q schedule %q: %v", storage.StorageRef.Name, schedule.Name, err)}
			}
			keep, err := scheduleKeep(schedule)
			if err != nil {
				return &controller.BackupConfigError{Reason: "RetentionTypeUnsupported", Message: err.Error()}
			}
			backupSpec.Schedule = append(backupSpec.Schedule, psv1.BackupSchedule{
				Name:        schedule.Name,
				Schedule:    schedule.Cron,
				StorageName: storage.StorageRef.Name,
				Keep:        keep,
				Type:        backupType,
			})
		}
	}

	cluster.Spec.Backup = backupSpec
	return nil
}

// renderBackupStorage builds the operator storage of an entry the runtime
// cleared to render. The operator speaks plain S3 to every S3
// implementation.
func renderBackupStorage(c *controller.Context, s controller.ResolvedBackupStorage) (*psv1.BackupStorageSpec, error) {
	s3, _, err := c.BackupStorageS3(s.Storage)
	if err != nil {
		return nil, err
	}
	tls, err := c.BackupStorageTLS(s.Storage, backupclasses.TLSSupport(backupClassName, s3class.ClassName))
	if err != nil {
		return nil, err
	}

	opStorage := &psv1.BackupStorageSpec{
		Type: psv1.BackupStorageS3,
		S3: &psv1.BackupStorageS3Spec{
			Bucket: psv1.BucketWithPrefix(s.Storage.Spec.Location.ObjectStore.Bucket),
			Prefix: s.Path,
			// The operator reads AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY from it.
			CredentialsSecret: s.Storage.Spec.Authentication.CredentialsSecretRef.Name,
			Region:            s3.Region,
			EndpointURL:       s3.EndpointURL,
		},
	}
	if tls.InsecureSkipVerify {
		opStorage.VerifyTLS = new(false)
	}
	if s3.ForcePathStyle {
		opStorage.ContainerOptions = &psv1.BackupContainerOptions{
			Env: []corev1.EnvVar{{
				Name:  "AWS_FORCE_PATH_STYLE",
				Value: "true",
			}},
		}
	}
	return opStorage, nil
}

// scheduleKeep returns the number of backups the operator keeps for the
// schedule, 0 (keep all) when it sets no retention. The PS operator only
// supports count-based retention.
func scheduleKeep(schedule corev1alpha1.InstanceBackupSchedule) (int, error) {
	if schedule.Retention == nil {
		return 0, nil
	}
	if schedule.Retention.Type != corev1alpha1.BackupScheduleRetentionTypeCount {
		return 0, fmt.Errorf("schedule %q: %s retention is not supported by Percona Server for MySQL, use count", schedule.Name, schedule.Retention.Type)
	}
	return int(*schedule.Retention.Count), nil
}

// resolveBackupImage resolves the xtrabackup image to use for backups and
// restores: the version bundle selected on the Instance (components.backup),
// falling back to the provider's default "backup" componentType version.
func resolveBackupImage(c *controller.Context, providerSpec *corev1alpha1.ProviderSpec) string {
	selectedBundle := controller.EffectiveVersionBundleName(providerSpec, c.Instance())
	if selectedBundle != "" {
		if bundle, err := controller.ResolveVersionBundle(providerSpec, selectedBundle); err == nil {
			if backupVersion, ok := bundle.Components[common.ComponentBackup]; ok && backupVersion != "" {
				if image := controller.GetImageForVersion(providerSpec, common.ComponentBackup, backupVersion); image != "" {
					return image
				}
			}
		}
	}
	return controller.GetDefaultImage(providerSpec, common.ComponentBackup)
}

// OperatorBackupType implements controller.BackupMirror (optional).
func (p *Provider) OperatorBackupType() client.Object {
	return &psv1.PerconaServerMySQLBackup{}
}
