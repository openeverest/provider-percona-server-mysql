package provider

import (
	"context"
	"testing"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const (
	bsTestInstance    = "db"
	bsTestNamespace   = "team-a"
	bsTestCredentials = "s3-credentials"
	bsTestCA          = "s3-ca"
	bsTestS3          = `{"region":"us-east-1","endpointURL":"https://s3.us-east-1.amazonaws.com"}`
	bsTestBucket      = "acme-backups"
	bsTestPath        = "ps/backups/team-a/db/instance-uid/"

	bsTestClassUID        = "ps-uid"
	bsTestStorageClassUID = "s3-uid"
)

var bsTestProviderSpec = corev1alpha1.ProviderSpec{
	DefaultVersion: "8.4.7",
	Versions:       []corev1alpha1.VersionBundle{{Name: "8.4.7"}},
	ComponentTypes: map[string]corev1alpha1.ComponentType{
		common.ComponentBackup: {
			DefaultVersion: "8.4.0-6.1",
			Versions:       []corev1alpha1.ComponentVersion{{Version: "8.4.0-6.1", Image: "percona/percona-xtrabackup:8.4.0-6.1"}},
		},
	},
}

// backupContext returns the context of an Instance backing up to entries,
// with a client whose calls go through funcs; objs are what the cluster
// holds besides the Instance, its Provider, the ps BackupClass and the s3
// BackupStorageClass.
func backupContext(t *testing.T, entries []corev1alpha1.InstanceBackupStorage, funcs interceptor.Funcs, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, psv1.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: bsTestInstance, Namespace: bsTestNamespace, UID: "instance-uid"},
		Spec: corev1alpha1.InstanceSpec{
			Backup: &corev1alpha1.InstanceBackupSpec{
				Enabled:  true,
				ClassRef: commonv1alpha1.ObjectRef{Name: backupClassName},
				Storages: entries,
			},
		},
	}
	provider := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: common.ProviderName},
		Spec:       bsTestProviderSpec,
	}
	class := &backupv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: backupClassName, UID: bsTestClassUID, Generation: 1},
		Spec: backupv1alpha1.BackupClassSpec{
			ExecutionMode: backupv1alpha1.BackupExecutionModeProviderManaged,
			BackupStorageClasses: []backupv1alpha1.BackupStorageClassSupport{
				{Name: "s3", AuthModes: []string{"AccessKey"}},
			},
		},
	}
	storageClass := &backupv1alpha1.BackupStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "s3", UID: bsTestStorageClassUID, Generation: 1},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, in, provider, class, storageClass)...).WithInterceptorFuncs(funcs).Build()
	return controller.NewContext(context.Background(), cl, in, provider.Name)
}

func bsEntry(name string, schedules ...string) corev1alpha1.InstanceBackupStorage {
	entry := corev1alpha1.InstanceBackupStorage{StorageRef: commonv1alpha1.ObjectRef{Name: name}}
	for _, s := range schedules {
		entry.Schedules = append(entry.Schedules, corev1alpha1.InstanceBackupSchedule{Name: s, Enabled: true, Cron: "0 2 * * *"})
	}
	return entry
}

// bsStorage is an s3 BackupStorage in acme-backups/ps. When accepted is
// set, it is Accepted for its generation and core has judged it Compatible
// with the ps BackupClass.
func bsStorage(name string, accepted bool, parameters string, tls *commonv1alpha1.TLSConfig) *backupv1alpha1.BackupStorage {
	bs := &backupv1alpha1.BackupStorage{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: bsTestNamespace, Generation: 1},
		Spec: backupv1alpha1.BackupStorageSpec{
			BackupStorageClassRef: commonv1alpha1.ObjectRef{Name: "s3"},
			Location:              backupv1alpha1.BackupStorageLocation{ObjectStore: &backupv1alpha1.ObjectStoreLocation{Bucket: bsTestBucket, Prefix: "ps"}},
			Parameters:            &runtime.RawExtension{Raw: []byte(parameters)},
			Authentication: commonv1alpha1.Authentication{
				Mode:                 "AccessKey",
				CredentialsSecretRef: &commonv1alpha1.SecretRef{Name: bsTestCredentials},
			},
			TLS: tls,
		},
	}
	if accepted {
		bs.Status.Conditions = []metav1.Condition{{
			Type:               backupv1alpha1.BackupStorageConditionAccepted,
			Status:             metav1.ConditionTrue,
			Reason:             backupv1alpha1.ReasonAccepted,
			ObservedGeneration: 1,
		}}
		bs.Status.BackupClasses = []backupv1alpha1.BackupClassVerdict{{
			Name:          backupClassName,
			Compatibility: backupv1alpha1.CompatibilityCompatible,
			Observed: backupv1alpha1.BackupClassVerdictInputs{
				StorageGeneration:      1,
				ClassUID:               bsTestClassUID,
				ClassGeneration:        1,
				StorageClassUID:        bsTestStorageClassUID,
				StorageClassGeneration: 1,
			},
		}}
	}
	return bs
}

func bsSecrets() []client.Object {
	return []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: bsTestCredentials, Namespace: bsTestNamespace},
			Data:       map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("id"), "AWS_SECRET_ACCESS_KEY": []byte("secret")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: bsTestCA, Namespace: bsTestNamespace},
			Data:       map[string][]byte{"ca.crt": []byte("-----BEGIN CERTIFICATE-----")},
		},
	}
}

// bsHeld is the storage block a previous Sync rendered for name.
func bsHeld(name string) *psv1.BackupStorageSpec {
	return &psv1.BackupStorageSpec{
		Type: psv1.BackupStorageS3,
		S3: &psv1.BackupStorageS3Spec{
			Bucket:            "old-bucket",
			Prefix:            name,
			CredentialsSecret: "old-credentials",
			Region:            "eu-west-1",
		},
	}
}

func bsLiveCluster(storages ...string) *psv1.PerconaServerMySQL {
	live := &psv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: bsTestInstance, Namespace: bsTestNamespace},
		Spec: psv1.PerconaServerMySQLSpec{
			Backup: &psv1.BackupSpec{Storages: map[string]*psv1.BackupStorageSpec{}},
		},
	}
	for _, name := range storages {
		live.Spec.Backup.Storages[name] = bsHeld(name)
	}
	return live
}

func bsReasons(t *testing.T, c *controller.Context) map[string]string {
	t.Helper()
	storages, err := c.BackupStorages()
	require.NoError(t, err)
	reasons := make(map[string]string, len(storages))
	for _, s := range storages {
		reasons[s.Name()] = s.Reason
	}
	return reasons
}

func TestBackupStorageRender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		parameters string
		tls        *commonv1alpha1.TLSConfig
		want       *psv1.BackupStorageSpec
	}{
		{
			name:       "backups are written under the instance's path in the bucket",
			parameters: bsTestS3,
			want: &psv1.BackupStorageSpec{
				Type: psv1.BackupStorageS3,
				S3: &psv1.BackupStorageS3Spec{
					Bucket:            bsTestBucket,
					Prefix:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "us-east-1",
					EndpointURL:       "https://s3.us-east-1.amazonaws.com",
				},
			},
		},
		{
			name:       "a gateway addressed by path gets path-style requests",
			parameters: `{"region":"us-east-1","endpointURL":"http://minio.storage:9000","forcePathStyle":true,"implementation":"minio"}`,
			want: &psv1.BackupStorageSpec{
				Type: psv1.BackupStorageS3,
				S3: &psv1.BackupStorageS3Spec{
					Bucket:            bsTestBucket,
					Prefix:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "us-east-1",
					EndpointURL:       "http://minio.storage:9000",
				},
				ContainerOptions: &psv1.BackupContainerOptions{
					Env: []corev1.EnvVar{{Name: "AWS_FORCE_PATH_STYLE", Value: "true"}},
				},
			},
		},
		{
			name:       "gcs is spoken to as plain s3",
			parameters: `{"region":"auto","endpointURL":"https://storage.googleapis.com","implementation":"gcs"}`,
			want: &psv1.BackupStorageSpec{
				Type: psv1.BackupStorageS3,
				S3: &psv1.BackupStorageS3Spec{
					Bucket:            bsTestBucket,
					Prefix:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "auto",
					EndpointURL:       "https://storage.googleapis.com",
				},
			},
		},
		{
			name:       "skipping certificate verification turns verifyTLS off",
			parameters: bsTestS3,
			tls:        &commonv1alpha1.TLSConfig{InsecureSkipVerify: true},
			want: &psv1.BackupStorageSpec{
				Type: psv1.BackupStorageS3,
				S3: &psv1.BackupStorageS3Spec{
					Bucket:            bsTestBucket,
					Prefix:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "us-east-1",
					EndpointURL:       "https://s3.us-east-1.amazonaws.com",
				},
				VerifyTLS: new(false),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			objs := append(bsSecrets(), bsStorage("main", true, tt.parameters, tt.tls))
			c := backupContext(t, []corev1alpha1.InstanceBackupStorage{bsEntry("main", "daily")}, interceptor.Funcs{}, objs...)
			cluster := &psv1.PerconaServerMySQL{}

			require.NoError(t, applyBackupSettings(c, cluster))

			require.NotNil(t, cluster.Spec.Backup)
			assert.Equal(t, "percona/percona-xtrabackup:8.4.0-6.1", cluster.Spec.Backup.Image)
			assert.Equal(t, map[string]*psv1.BackupStorageSpec{"main": tt.want}, cluster.Spec.Backup.Storages)
			require.Len(t, cluster.Spec.Backup.Schedule, 1)
			assert.Equal(t, "main", cluster.Spec.Backup.Schedule[0].StorageName)
			assert.Equal(t, map[string]string{"main": ""}, bsReasons(t, c))
		})
	}
}

func TestBackupStorageHoldRule(t *testing.T) {
	t.Parallel()

	customCA := &commonv1alpha1.TLSConfig{CA: &commonv1alpha1.CABundleSource{
		SecretKeyRef: &commonv1alpha1.SecretKeyRef{Name: bsTestCA, Key: "ca.crt"},
	}}

	tests := []struct {
		name          string
		storage       *backupv1alpha1.BackupStorage
		live          *psv1.PerconaServerMySQL
		wantStorages  map[string]*psv1.BackupStorageSpec
		wantSchedules int
		wantReason    string
	}{
		{
			name:          "a storage that is not Accepted keeps the live block",
			storage:       bsStorage("main", false, bsTestS3, nil),
			live:          bsLiveCluster("main"),
			wantStorages:  map[string]*psv1.BackupStorageSpec{"main": bsHeld("main")},
			wantSchedules: 1,
			wantReason:    corev1alpha1.ReasonBackupStorageNotAccepted,
		},
		{
			name:         "a storage that is not Accepted is not added",
			storage:      bsStorage("main", false, bsTestS3, nil),
			wantStorages: map[string]*psv1.BackupStorageSpec{},
			wantReason:   corev1alpha1.ReasonBackupStorageNotAccepted,
		},
		{
			name:          "a missing storage keeps the live block",
			live:          bsLiveCluster("main"),
			wantStorages:  map[string]*psv1.BackupStorageSpec{"main": bsHeld("main")},
			wantSchedules: 1,
			wantReason:    corev1alpha1.ReasonBackupStorageNotFound,
		},
		{
			name:          "a parameter the provider does not know is rejected and held",
			storage:       bsStorage("main", true, `{"region":"us-east-1","endpointURL":"https://s3.us-east-1.amazonaws.com","sse":"aws:kms"}`, nil),
			live:          bsLiveCluster("main"),
			wantStorages:  map[string]*psv1.BackupStorageSpec{"main": bsHeld("main")},
			wantSchedules: 1,
			wantReason:    corev1alpha1.ReasonUnsupportedParameter,
		},
		{
			name:          "a custom CA the operator cannot verify against is rejected and held",
			storage:       bsStorage("main", true, bsTestS3, customCA),
			live:          bsLiveCluster("main"),
			wantStorages:  map[string]*psv1.BackupStorageSpec{"main": bsHeld("main")},
			wantSchedules: 1,
			wantReason:    corev1alpha1.ReasonTLSOptionUnsupported,
		},
		{
			name:         "a custom CA is rejected and not added",
			storage:      bsStorage("main", true, bsTestS3, customCA),
			wantStorages: map[string]*psv1.BackupStorageSpec{},
			wantReason:   corev1alpha1.ReasonTLSOptionUnsupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			objs := bsSecrets()
			if tt.storage != nil {
				objs = append(objs, tt.storage)
			}
			if tt.live != nil {
				objs = append(objs, tt.live)
			}
			c := backupContext(t, []corev1alpha1.InstanceBackupStorage{bsEntry("main", "daily")}, interceptor.Funcs{}, objs...)
			cluster := &psv1.PerconaServerMySQL{}

			require.NoError(t, applyBackupSettings(c, cluster))

			require.NotNil(t, cluster.Spec.Backup)
			assert.Equal(t, tt.wantStorages, cluster.Spec.Backup.Storages)
			assert.Len(t, cluster.Spec.Backup.Schedule, tt.wantSchedules, "schedules only target storages the engine has")
			assert.Equal(t, map[string]string{"main": tt.wantReason}, bsReasons(t, c))
		})
	}
}

func TestBackupStorageAPIErrorsFailSync(t *testing.T) {
	t.Parallel()

	funcs := interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == bsTestCredentials {
				return apierrors.NewServiceUnavailable("etcd is down")
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	}
	objs := append(bsSecrets(), bsStorage("main", true, bsTestS3, nil), bsLiveCluster("main"))
	c := backupContext(t, []corev1alpha1.InstanceBackupStorage{bsEntry("main")}, funcs, objs...)

	err := applyBackupSettings(c, &psv1.PerconaServerMySQL{})

	require.Error(t, err)
	assert.True(t, apierrors.IsServiceUnavailable(err))
	assert.Equal(t, map[string]string{"main": ""}, bsReasons(t, c), "a transient error is not a verdict on the storage")
}
