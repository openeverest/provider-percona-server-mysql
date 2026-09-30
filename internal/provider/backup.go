package provider

import (
	"context"
	"encoding/json"
	"fmt"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// instanceNameLabelKey is stamped on Backup/Restore CRs so they can be listed
// by the Instance that owns them (e.g. `kubectl get backups -l instanceName=foo`).
const instanceNameLabelKey = "instanceName"

// defaultBackupType is used when a Backup or InstanceBackupSchedule does not
// set spec.parameters.type.
const defaultBackupType = psv1.BackupTypeFull

// Compile-time interface checks.
var _ controller.BackupProvider = (*Provider)(nil)
var _ controller.BackupWatcher = (*Provider)(nil)
var _ controller.RestoreWatcher = (*Provider)(nil)

// SyncBackup creates or updates the operator's backup resource, sets a controller
// reference from the Backup CR to enable owner-based watches, and maps operator
// status to OpenEverest states.
func (p *Provider) SyncBackup(c *controller.Context, backup *backupv1alpha1.Backup) (controller.BackupExecutionStatus, error) {
	l := log.FromContext(c.Context())
	l.Info("Syncing backup", "name", backup.Name)

	if backup.Labels == nil {
		backup.Labels = map[string]string{}
	}
	if backup.Labels[instanceNameLabelKey] != backup.Spec.Origin.InstanceRef.Name {
		origBackup := backup.DeepCopy()
		backup.Labels[instanceNameLabelKey] = backup.Spec.Origin.InstanceRef.Name
		if err := c.Client().Patch(c.Context(), backup, client.MergeFrom(origBackup)); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("patch Backup %q labels: %w", backup.Name, err)
		}
		// Re-fetch the backup to ensure we have the latest resource version
		// before using it as a controller reference owner.
		if err := c.Client().Get(c.Context(), client.ObjectKeyFromObject(backup), backup); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("re-fetch Backup %q after label patch: %w", backup.Name, err)
		}
	}

	opRef := &apicommon.TypedObjectRef{
		Group: psv1.GroupVersion.Group,
		Kind:  "PerconaServerMySQLBackup",
		Name:  backup.Name,
	}
	managedByRuntime := backup.Spec.ScheduleName == ""
	ensureBackupControllerReference := func(opBackup *psv1.PerconaServerMySQLBackup) error {
		if err := controllerutil.SetControllerReference(backup, opBackup, c.Client().Scheme()); err != nil {
			return fmt.Errorf("set backup controller reference: %w", err)
		}
		return nil
	}

	opBackup := &psv1.PerconaServerMySQLBackup{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Name}, opBackup)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaServerMySQLBackup %q: %w", backup.Name, err)
		}

		if !managedByRuntime {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStatePending,
				Message:           "Waiting for operator scheduled backup",
				OperatorBackupRef: opRef,
			}, nil
		}

		cluster := &psv1.PerconaServerMySQL{}
		if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Spec.Origin.InstanceRef.Name}, cluster); err != nil {
			if apierrors.IsNotFound(err) {
				return controller.BackupExecutionStatus{
					State:             backupv1alpha1.BackupStatePending,
					Message:           "Waiting for PerconaServerMySQL",
					OperatorBackupRef: opRef,
				}, nil
			}
			return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaServerMySQL %q: %w", backup.Spec.Origin.InstanceRef.Name, err)
		}

		// The Instance reconciler is what registers storages on the
		// PerconaServerMySQL CR, so a Backup created before (or concurrently
		// with) that write legitimately observes them missing. Wait instead
		// of failing: BackupStateFailed is terminal in the runtime, which
		// would turn a startup race into a permanently failed backup.
		storages := map[string]*psv1.BackupStorageSpec{}
		if cluster.Spec.Backup != nil {
			storages = cluster.Spec.Backup.Storages
		}
		if _, ok := storages[backup.Spec.StorageRef.Name]; !ok {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStatePending,
				Message:           fmt.Sprintf("Waiting for storage %q to be registered on PerconaServerMySQL cluster", backup.Spec.StorageRef.Name),
				OperatorBackupRef: opRef,
			}, nil
		}

		backupType, err := resolveBackupType(backup.Spec.Parameters)
		if err != nil {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStateFailed,
				Message:           err.Error(),
				OperatorBackupRef: opRef,
			}, nil
		}

		opBackup = &psv1.PerconaServerMySQLBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backup.Name,
				Namespace: backup.Namespace,
			},
			Spec: psv1.PerconaServerMySQLBackupSpec{
				Type:        backupType,
				ClusterName: backup.Spec.Origin.InstanceRef.Name,
				StorageName: backup.Spec.StorageRef.Name,
			},
		}
		if !c.ShouldRetainBackupData(backup) {
			opBackup.Finalizers = []string{naming.FinalizerDeleteBackup}
		}
		if err := ensureBackupControllerReference(opBackup); err != nil {
			return controller.BackupExecutionStatus{}, err
		}
		if err := c.Client().Create(c.Context(), opBackup); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return controller.BackupExecutionStatus{}, fmt.Errorf("create PerconaServerMySQLBackup %q: %w", backup.Name, err)
			}
			if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Name}, opBackup); err != nil {
				return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaServerMySQLBackup %q after AlreadyExists: %w", backup.Name, err)
			}
		}
	}

	if managedByRuntime {
		origBackup := opBackup.DeepCopy()
		if immutableChangeMsg := immutableBackupSpecChangeMessage(opBackup, backup); immutableChangeMsg != "" {
			immutableErr := fmt.Errorf("cannot change immutable backup spec")
			l.Error(
				immutableErr,
				"failed to reconcile backup CR",
				"backup", backup.Name,
				"requestedInstanceName", backup.Spec.Origin.InstanceRef.Name,
				"existingInstanceName", opBackup.Spec.ClusterName,
				"requestedStorageName", backup.Spec.StorageRef.Name,
				"existingStorageName", opBackup.Spec.StorageName,
				"reason", immutableChangeMsg,
			)
		}
		if c.ShouldRetainBackupData(backup) {
			controllerutil.RemoveFinalizer(opBackup, naming.FinalizerDeleteBackup)
		} else {
			controllerutil.AddFinalizer(opBackup, naming.FinalizerDeleteBackup)
		}
		if err := ensureBackupControllerReference(opBackup); err != nil {
			return controller.BackupExecutionStatus{}, err
		}
		if err := c.Client().Patch(c.Context(), opBackup, client.MergeFrom(origBackup)); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("patch PerconaServerMySQLBackup %q: %w", backup.Name, err)
		}
	}

	exec := controller.BackupExecutionStatus{
		OperatorBackupRef: opRef,
		Message:           opBackup.Status.StateDesc,
	}

	if !opBackup.CreationTimestamp.IsZero() {
		t := opBackup.CreationTimestamp
		exec.StartedAt = &t
	}

	switch opBackup.Status.State {
	case psv1.BackupFailed, psv1.BackupError:
		exec.State = backupv1alpha1.BackupStateFailed
		if opBackup.Status.StateDesc != "" {
			exec.Message = opBackup.Status.StateDesc
		} else {
			exec.Message = "Backup failed"
		}
	case psv1.BackupSucceeded:
		exec.State = backupv1alpha1.BackupStateSucceeded
		exec.CompletedAt = opBackup.Status.CompletedAt
		exec.Message = "Backup completed"
	case psv1.BackupRunning, psv1.BackupStarting:
		exec.State = backupv1alpha1.BackupStateRunning
		exec.Message = "Backup is running"
	default:
		exec.State = backupv1alpha1.BackupStatePending
		exec.Message = "Backup is pending"
	}

	return exec, nil
}

// SyncRestore resolves the source Backup CR, creates or updates the operator's
// restore resource with a controller reference, and maps operator status to
// OpenEverest states.
func (p *Provider) SyncRestore(c *controller.Context, restore *backupv1alpha1.Restore) (controller.RestoreExecutionStatus, error) {
	l := log.FromContext(c.Context())
	l.Info("Syncing restore", "name", restore.Name)

	if restore.Labels == nil {
		restore.Labels = map[string]string{}
	}
	if restore.Labels[instanceNameLabelKey] != restore.Spec.InstanceRef.Name {
		origRestore := restore.DeepCopy()
		restore.Labels[instanceNameLabelKey] = restore.Spec.InstanceRef.Name
		if err := c.Client().Patch(c.Context(), restore, client.MergeFrom(origRestore)); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("patch Restore %q labels: %w", restore.Name, err)
		}
		// Re-fetch the restore to ensure we have the latest resource version
		// before using it as a controller reference owner.
		if err := c.Client().Get(c.Context(), client.ObjectKeyFromObject(restore), restore); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("re-fetch Restore %q after label patch: %w", restore.Name, err)
		}
	}

	opRef := &apicommon.TypedObjectRef{
		Group: psv1.GroupVersion.Group,
		Kind:  "PerconaServerMySQLRestore",
		Name:  restore.Name,
	}

	backupName, pending, err := resolveRestoreSource(c, restore, opRef)
	if err != nil {
		return controller.RestoreExecutionStatus{}, err
	}
	if pending != nil {
		return *pending, nil
	}

	opRestore := &psv1.PerconaServerMySQLRestore{}
	err = c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: restore.Name}, opRestore)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("get PerconaServerMySQLRestore %q: %w", restore.Name, err)
		}

		opRestore = &psv1.PerconaServerMySQLRestore{
			ObjectMeta: metav1.ObjectMeta{Name: restore.Name, Namespace: restore.Namespace},
			Spec: psv1.PerconaServerMySQLRestoreSpec{
				ClusterName: restore.Spec.InstanceRef.Name,
				BackupName:  backupName,
			},
		}
		if err := controllerutil.SetControllerReference(restore, opRestore, c.Client().Scheme()); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("set restore controller reference: %w", err)
		}
		if err := c.Client().Create(c.Context(), opRestore); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return controller.RestoreExecutionStatus{}, fmt.Errorf("create PerconaServerMySQLRestore %q: %w", restore.Name, err)
			}
			if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: restore.Name}, opRestore); err != nil {
				return controller.RestoreExecutionStatus{}, fmt.Errorf("get PerconaServerMySQLRestore %q after AlreadyExists: %w", restore.Name, err)
			}
		}
	}

	origRestore := opRestore.DeepCopy()
	if err := controllerutil.SetControllerReference(restore, opRestore, c.Client().Scheme()); err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("set restore controller reference: %w", err)
	}
	if err := c.Client().Patch(c.Context(), opRestore, client.MergeFrom(origRestore)); err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("patch PerconaServerMySQLRestore %q: %w", restore.Name, err)
	}

	out := controller.RestoreExecutionStatus{
		OperatorRestoreRef: opRef,
		Message:            opRestore.Status.StateDesc,
	}

	if !opRestore.CreationTimestamp.IsZero() {
		t := opRestore.CreationTimestamp
		out.StartedAt = &t
	}

	switch opRestore.Status.State {
	case psv1.RestoreFailed, psv1.RestoreError:
		out.State = backupv1alpha1.RestoreStateFailed
		if opRestore.Status.StateDesc != "" {
			out.Message = opRestore.Status.StateDesc
		} else {
			out.Message = "Restore failed"
		}
	case psv1.RestoreSucceeded:
		out.State = backupv1alpha1.RestoreStateSucceeded
		out.CompletedAt = opRestore.Status.CompletedAt
		out.Message = "Restore completed"
	case psv1.RestoreStarting, psv1.RestoreRunning:
		out.State = backupv1alpha1.RestoreStateRunning
		out.Message = "Restore is running"
	default:
		out.State = backupv1alpha1.RestoreStatePending
		out.Message = "Restore is pending"
	}

	return out, nil
}

// resolveRestoreSource translates the Restore's data source into the name of
// the PerconaServerMySQLBackup to restore from. This BackupClass does not
// advertise PITR support (see definition/backupclasses/ps/class.yaml), so
// only DataSourceTypeBackup is accepted.
//
// A non-nil RestoreExecutionStatus means the source is not usable yet (or at
// all) and the caller should surface that status verbatim.
func resolveRestoreSource(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
	opRef *apicommon.TypedObjectRef,
) (string, *controller.RestoreExecutionStatus, error) {
	if restore.Spec.DataSource.Type != backupv1alpha1.DataSourceTypeBackup {
		return "", &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            fmt.Sprintf("Unsupported dataSource type %q", restore.Spec.DataSource.Type),
			OperatorRestoreRef: opRef,
		}, nil
	}

	ref := restore.Spec.DataSource.Backup
	if ref == nil || ref.BackupRef.Name == "" {
		return "", &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            "Restore dataSource.backup.backupRef.name is required",
			OperatorRestoreRef: opRef,
		}, nil
	}

	sourceBackup := &backupv1alpha1.Backup{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: ref.BackupRef.Name}, sourceBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return "", &controller.RestoreExecutionStatus{
				State:              backupv1alpha1.RestoreStatePending,
				Message:            "Waiting for source Backup",
				OperatorRestoreRef: opRef,
			}, nil
		}
		return "", nil, fmt.Errorf("get source Backup %q: %w", ref.BackupRef.Name, err)
	}

	if sourceBackup.Status.State == backupv1alpha1.BackupStateFailed {
		return "", &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            "Source Backup failed; cannot restore",
			OperatorRestoreRef: opRef,
		}, nil
	}

	sourceInstanceName := ""
	if sourceBackup.Spec.Origin.InstanceRef != nil {
		sourceInstanceName = sourceBackup.Spec.Origin.InstanceRef.Name
	}
	if sourceInstanceName != restore.Spec.InstanceRef.Name {
		return "", &controller.RestoreExecutionStatus{
			State: backupv1alpha1.RestoreStateFailed,
			Message: fmt.Sprintf(
				"Restore from Backup %q onto Instance %q is not supported; the Backup belongs to Instance %q",
				sourceBackup.Name, restore.Spec.InstanceRef.Name, sourceInstanceName,
			),
			OperatorRestoreRef: opRef,
		}, nil
	}

	return sourceBackup.Name, nil, nil
}

// CleanupBackup deletes the operator backup resource.
// When the Backup's DeletionPolicy is Retain, the operator's
// percona.com/delete-backup finalizer is removed first so the operator skips
// data purging and leaves the backup data in storage. When the policy is
// Delete (default), the finalizer is left in place so the operator cleans up
// the data.
// Return true only when fully deleted, false to requeue.
func (p *Provider) CleanupBackup(c *controller.Context, backup *backupv1alpha1.Backup) (bool, error) {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up backup", "name", backup.Name, "deletionPolicy", backup.Spec.DeletionPolicy)

	name := backup.Name

	opBackup := &psv1.PerconaServerMySQLBackup{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: name}, opBackup)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get PerconaServerMySQLBackup %q: %w", name, err)
	}

	// When the user wants to retain backup data, strip the operator's
	// delete-backup finalizer so it won't purge data from storage.
	if c.ShouldRetainBackupData(backup) {
		if controllerutil.RemoveFinalizer(opBackup, naming.FinalizerDeleteBackup) {
			if err := c.Client().Update(c.Context(), opBackup); err != nil {
				return false, fmt.Errorf("remove delete-backup finalizer from PerconaServerMySQLBackup %q: %w", name, err)
			}
		}
	}

	if opBackup.DeletionTimestamp.IsZero() {
		if err := c.Client().Delete(c.Context(), opBackup); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete PerconaServerMySQLBackup %q: %w", name, err)
		}
	}

	return false, nil
}

// CleanupRestore deletes the operator restore resource. Return true when fully
// deleted, false to requeue.
func (p *Provider) CleanupRestore(c *controller.Context, restore *backupv1alpha1.Restore) (bool, error) {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up restore", "name", restore.Name)

	name := restore.Name

	opRestore := &psv1.PerconaServerMySQLRestore{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: name}, opRestore)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get PerconaServerMySQLRestore %q: %w", name, err)
	}

	if opRestore.DeletionTimestamp.IsZero() {
		if err := c.Client().Delete(c.Context(), opRestore); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete PerconaServerMySQLRestore %q: %w", name, err)
		}
	}

	return false, nil
}

// BackupWatches implements controller.BackupWatcher. Register watches so operator
// backup status changes trigger reconciliation. SyncBackup sets a controller
// reference from the Backup CR to the PerconaServerMySQLBackup, so the runtime
// resolves the owning Backup itself from the event.
func (p *Provider) BackupWatches() []controller.WatchConfig {
	return []controller.WatchConfig{
		controller.WatchExternal(
			&psv1.PerconaServerMySQLBackup{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
			}),
			controller.ResourceVersionChangedPredicate,
		),
	}
}

// hasActiveRestoreForInstance reports whether the namespace has at least one
// non-terminal Restore for the given instance. The PS operator toggles
// cluster.Spec.Pause directly while a PerconaServerMySQLRestore runs (see
// psrestore controller), so Sync must not fight that by re-asserting its own
// spec while a restore is in flight.
func hasActiveRestoreForInstance(c *controller.Context, namespace, instanceName string) (bool, error) {
	restoreList := &backupv1alpha1.RestoreList{}
	if err := c.Client().List(
		c.Context(),
		restoreList,
		client.InNamespace(namespace),
	); err != nil {
		return false, fmt.Errorf("list Restore resources for instance %q: %w", instanceName, err)
	}

	for i := range restoreList.Items {
		r := restoreList.Items[i]
		if r.Spec.InstanceRef.Name != instanceName || !r.DeletionTimestamp.IsZero() {
			continue
		}
		switch r.Status.State {
		case backupv1alpha1.RestoreStateSucceeded, backupv1alpha1.RestoreStateFailed:
			continue
		default:
			return true, nil
		}
	}

	return false, nil
}

// enqueueRestoreInstance maps a Restore event to a reconcile request for the
// Instance it targets, so the Instance phase tracks the restore's lifecycle.
func enqueueRestoreInstance() func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		r, ok := obj.(*backupv1alpha1.Restore)
		if !ok || r.Spec.InstanceRef.Name == "" {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: r.Namespace,
				Name:      r.Spec.InstanceRef.Name,
			},
		}}
	}
}

// RestoreWatches implements controller.RestoreWatcher. Register watches so operator
// restore status changes trigger reconciliation. Use WatchOwned for resources with
// controller references set by SyncRestore.
func (p *Provider) RestoreWatches() []controller.WatchConfig {
	return []controller.WatchConfig{
		controller.WatchExternal(
			&psv1.PerconaServerMySQLRestore{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
			}),
			controller.ResourceVersionChangedPredicate,
		),
	}
}

func immutableBackupSpecChangeMessage(opBackup *psv1.PerconaServerMySQLBackup, backup *backupv1alpha1.Backup) string {
	if backup.Spec.Origin.InstanceRef.Name != opBackup.Spec.ClusterName {
		return fmt.Sprintf(
			"cannot change backup spec.origin.instanceRef.name after creation (requested %q, existing %q)",
			backup.Spec.Origin.InstanceRef.Name,
			opBackup.Spec.ClusterName,
		)
	}
	if backup.Spec.StorageRef.Name != opBackup.Spec.StorageName {
		return fmt.Sprintf(
			"cannot change backup spec.storageRef.name after creation (requested %q, existing %q)",
			backup.Spec.StorageRef.Name,
			opBackup.Spec.StorageName,
		)
	}

	return ""
}

// psBackupTypeParameters is the subset of definition/backupclasses/ps.
// PsBackupParameters (and, for schedules, the identically-shaped per-storage
// schedule parameters) that resolveBackupType needs to decode.
type psBackupTypeParameters struct {
	Type psv1.BackupType `json:"type,omitempty"`
}

// resolveBackupType extracts the xtrabackup backup type from a Backup's
// spec.parameters or an InstanceBackupSchedule's spec.parameters — both are
// validated against the same BackupClass parametersSchema (PsBackupParameters).
func resolveBackupType(raw *runtime.RawExtension) (psv1.BackupType, error) {
	if raw == nil || len(raw.Raw) == 0 {
		return defaultBackupType, nil
	}
	var cfg psBackupTypeParameters
	if err := json.Unmarshal(raw.Raw, &cfg); err != nil {
		return "", fmt.Errorf("decode backup parameters: %w", err)
	}
	switch cfg.Type {
	case "":
		return defaultBackupType, nil
	case psv1.BackupTypeFull, psv1.BackupTypeIncremental:
		return cfg.Type, nil
	default:
		return "", fmt.Errorf("unsupported backup type %q; must be %q or %q", cfg.Type, psv1.BackupTypeFull, psv1.BackupTypeIncremental)
	}
}
