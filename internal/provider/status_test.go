package provider

import (
	"context"
	"encoding/json"
	"testing"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const (
	stTestName      = "my-ps"
	stTestNamespace = "db"
	stTestUID       = types.UID("6f0c5a3e-cluster")
	stTestImage     = "percona/percona-server:8.4.10-10.1"
	stTestOldImage  = "percona/percona-server:8.4.8-8.1"
	stTestHAProxy   = "percona/haproxy:2.8.18-1"
	stTestRouter    = "percona/percona-mysql-router:8.4.10"
	stTestOrc       = "percona/percona-orchestrator:3.2.6-22"
	stTestHost      = "my-ps-haproxy.db"
	stTestSecrets   = "my-ps-secrets"
)

// statusInstance is a three-node group-replication Instance behind one
// HAProxy pod.
func statusInstance(changes ...func(*corev1alpha1.InstanceSpec)) corev1alpha1.InstanceSpec {
	spec := corev1alpha1.InstanceSpec{
		Topology: &corev1alpha1.TopologySpec{Type: common.TopologyGroupReplication},
		Components: map[string]corev1alpha1.ComponentSpec{
			common.ComponentEngine: {Type: common.ComponentTypeMysql, Replicas: new(int32(3))},
			common.ComponentProxy:  {Type: common.ProxyTypeHAProxy, Replicas: new(int32(1))},
		},
	}
	for _, change := range changes {
		change(&spec)
	}
	return spec
}

// statusContext builds a context for an Instance named my-ps and seeds the
// cluster with objs, plus the users Secret the connection details are read
// from unless noUsers is set.
func statusContext(t *testing.T, spec corev1alpha1.InstanceSpec, objs ...client.Object) *controller.Context {
	t.Helper()
	users := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: stTestSecrets, Namespace: stTestNamespace},
		Data:       map[string][]byte{string(psv1.UserRoot): []byte("secret")},
	}
	return statusContextWithout(t, spec, append(objs, users)...)
}

func statusContextWithout(t *testing.T, spec corev1alpha1.InstanceSpec, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, psv1.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: stTestName, Namespace: stTestNamespace},
		Spec:       spec,
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, in)...).Build()
	return controller.NewContext(context.Background(), cl, in, common.ProviderName)
}

// readyCluster returns a three-node group-replication cluster behind one
// HAProxy pod that the operator reports ready. Tests change what they are
// about.
func readyCluster(changes ...func(*psv1.PerconaServerMySQL)) *psv1.PerconaServerMySQL {
	cluster := &psv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: stTestName, Namespace: stTestNamespace, UID: stTestUID, Generation: 4},
		Spec: psv1.PerconaServerMySQLSpec{
			SecretsName: stTestSecrets,
			MySQL: psv1.MySQLSpec{
				ClusterType: psv1.ClusterTypeGR,
				VolumeSpec: &psv1.VolumeSpec{PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("20Gi"),
					}},
				}},
				PodSpec: psv1.PodSpec{
					Size:          3,
					ContainerSpec: psv1.ContainerSpec{Image: stTestImage},
				},
			},
			Proxy: psv1.ProxySpec{
				HAProxy: &psv1.HAProxySpec{Enabled: true, PodSpec: psv1.PodSpec{Size: 1, ContainerSpec: psv1.ContainerSpec{Image: stTestHAProxy}}},
				Router:  &psv1.MySQLRouterSpec{Enabled: false},
			},
		},
		Status: psv1.PerconaServerMySQLStatus{
			State:   psv1.StateReady,
			Host:    stTestHost,
			MySQL:   psv1.StatefulAppStatus{Size: 3, Ready: 3, State: psv1.StateReady, Version: "8.4.10-10"},
			HAProxy: psv1.StatefulAppStatus{Size: 1, Ready: 1, State: psv1.StateReady},
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready"},
			},
		},
	}
	for _, change := range changes {
		change(cluster)
	}
	return cluster
}

// withRouter switches the cluster from HAProxy to MySQL Router.
func withRouter(cluster *psv1.PerconaServerMySQL) {
	cluster.Spec.Proxy.HAProxy = &psv1.HAProxySpec{Enabled: false}
	cluster.Spec.Proxy.Router = &psv1.MySQLRouterSpec{Enabled: true, PodSpec: psv1.PodSpec{Size: 2, ContainerSpec: psv1.ContainerSpec{Image: stTestRouter}}}
	cluster.Status.HAProxy = psv1.StatefulAppStatus{}
	cluster.Status.Router = psv1.StatefulAppStatus{Size: 2, Ready: 2, State: psv1.StateReady}
	cluster.Status.Host = "my-ps-router.db"
}

// asyncWithOrchestrator switches the cluster to asynchronous replication
// with three orchestrator pods.
func asyncWithOrchestrator(cluster *psv1.PerconaServerMySQL) {
	cluster.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
	cluster.Spec.Orchestrator = psv1.OrchestratorSpec{Enabled: true, PodSpec: psv1.PodSpec{Size: 3, ContainerSpec: psv1.ContainerSpec{Image: stTestOrc}}}
	cluster.Status.Orchestrator = psv1.StatefulAppStatus{Size: 3, Ready: 3, State: psv1.StateReady}
}

func clusterState(state psv1.StatefulAppState) func(*psv1.PerconaServerMySQL) {
	return func(cluster *psv1.PerconaServerMySQL) { cluster.Status.State = state }
}

// mysqlStatus changes what the operator publishes about the MySQL pods.
func mysqlStatus(change func(*psv1.StatefulAppStatus)) func(*psv1.PerconaServerMySQL) {
	return func(cluster *psv1.PerconaServerMySQL) { change(&cluster.Status.MySQL) }
}

// failing puts the cluster in the state the operator leaves it in when a pass
// over it fails.
func failing(reason, message string) func(*psv1.PerconaServerMySQL) {
	return func(cluster *psv1.PerconaServerMySQL) {
		cluster.Status.State = psv1.StateError
		cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
			Type: psv1.StateError.String(), Status: metav1.ConditionTrue, Reason: reason, Message: message,
		})
	}
}

// paused sets spec.pause and the state the operator reports once every pod
// is gone.
func paused(cluster *psv1.PerconaServerMySQL) {
	cluster.Spec.Pause = true
	cluster.Status.State = psv1.StatePaused
	cluster.Status.MySQL = psv1.StatefulAppStatus{Size: 3, State: psv1.StatePaused}
	cluster.Status.HAProxy = psv1.StatefulAppStatus{Size: 1, State: psv1.StatePaused}
}

func controlledBy(name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: psv1.GroupVersion.String(),
		Kind:       "PerconaServerMySQL",
		Name:       name,
		UID:        stTestUID,
		Controller: new(true),
	}}
}

func podSpec(container, image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: container, Image: image}}}}
}

// rolledOut returns one of the cluster's StatefulSets with every pod ready
// and running its latest template.
func rolledOut(component, image string, size int32, changes ...func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	name := stTestName + "-" + component
	set := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: stTestNamespace, Generation: 2, OwnerReferences: controlledBy(stTestName)},
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(size),
			Template: podSpec(component, image),
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2,
			Replicas:           size,
			ReadyReplicas:      size,
			UpdatedReplicas:    size,
			CurrentRevision:    name + "-2",
			UpdateRevision:     name + "-2",
		},
	}
	for _, change := range changes {
		change(set)
	}
	return set
}

// rolling has the StatefulSet roll out its template from its previous
// revision, with one of its pods updated.
func rolling(set *appsv1.StatefulSet) {
	set.Status.CurrentRevision = set.Name + "-1"
	set.Status.UpdatedReplicas = 1
	set.Status.ReadyReplicas = set.Status.Replicas - 1
}

// revision returns the ControllerRevision of the StatefulSet's pod template
// whose mysql container ran image.
func revision(t *testing.T, name, image string) *appsv1.ControllerRevision {
	t.Helper()
	data, err := json.Marshal(map[string]any{"spec": map[string]any{"template": podSpec(mysqlApp, image)}})
	require.NoError(t, err)
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: stTestNamespace},
		Data:       runtime.RawExtension{Raw: data},
		Revision:   1,
	}
}

func routerDeployment(changes ...func(*appsv1.Deployment)) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: stTestName + "-router", Namespace: stTestNamespace, Generation: 3, OwnerReferences: controlledBy(stTestName)},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(2)),
			Template: podSpec(routerApp, stTestRouter),
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2},
	}
	for _, change := range changes {
		change(d)
	}
	return d
}

func opRestore(name string, state psv1.RestoreState, created int64) *psv1.PerconaServerMySQLRestore {
	return &psv1.PerconaServerMySQLRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: stTestNamespace,
			CreationTimestamp: metav1.Unix(created, 0),
		},
		Spec:   psv1.PerconaServerMySQLRestoreSpec{ClusterName: stTestName, BackupName: "b1"},
		Status: psv1.PerconaServerMySQLRestoreStatus{State: state},
	}
}

func oeRestore(name string, state backupv1alpha1.RestoreState) *backupv1alpha1.Restore {
	r := &backupv1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: stTestNamespace},
	}
	r.Spec.InstanceRef.Name = stTestName
	r.Status.State = state
	return r
}

func problemReasons(st controller.Status) []string {
	var reasons []string
	for _, p := range st.Problems {
		reasons = append(reasons, p.Reason)
	}
	return reasons
}

func operationTypes(st controller.Status) []string {
	var types []string
	for _, op := range st.Operations {
		types = append(types, op.Type)
	}
	return types
}

func TestStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec corev1alpha1.InstanceSpec
		objs []client.Object
		// The status the provider reports.
		converged       bool
		message         string
		available       controller.Availability
		availableReason string
		problems        []string
		terminal        bool
		operations      []string
		connection      bool
	}{
		{
			name:    "the PerconaServerMySQL object is not created yet",
			message: "Waiting for the PerconaServerMySQL object to be created",
		},
		{
			name:    "the operator has not looked at the cluster yet",
			objs:    []client.Object{readyCluster(clusterState(""))},
			message: "Waiting for the operator to start creating the cluster",
		},
		{
			name: "coming up, with no proxy pod ready yet",
			objs: []client.Object{readyCluster(clusterState(psv1.StateInitializing), mysqlStatus(func(s *psv1.StatefulAppStatus) {
				s.Ready = 1
			}), func(cluster *psv1.PerconaServerMySQL) { cluster.Status.HAProxy.Ready = 0 })},
			message:         "Waiting for MySQL (1 of 3 ready); HAProxy (0 of 1 ready)",
			available:       controller.Unavailable,
			availableReason: reasonNoProxyReady,
		},
		{
			name: "coming up, with no MySQL pod ready yet",
			objs: []client.Object{readyCluster(clusterState(psv1.StateInitializing), mysqlStatus(func(s *psv1.StatefulAppStatus) {
				s.Ready = 0
			}))},
			message:         "Waiting for MySQL (0 of 3 ready)",
			available:       controller.Unavailable,
			availableReason: reasonNoNodeReady,
		},
		{
			name:       "every pod is ready but the operator waits for the group",
			objs:       []client.Object{readyCluster(clusterState(psv1.StateInitializing))},
			message:    "Waiting for the operator to report the cluster ready (it reports initializing)",
			available:  controller.Available,
			connection: true,
		},
		{
			name:       "ready and rolled out, behind HAProxy",
			objs:       []client.Object{readyCluster(), rolledOut(mysqlApp, stTestImage, 3), rolledOut(haproxyApp, stTestHAProxy, 1)},
			converged:  true,
			available:  controller.Available,
			connection: true,
		},
		{
			name:       "ready and rolled out, behind MySQL Router",
			objs:       []client.Object{readyCluster(withRouter), rolledOut(mysqlApp, stTestImage, 3), routerDeployment()},
			converged:  true,
			available:  controller.Available,
			connection: true,
		},
		{
			name: "ready and rolled out, asynchronous with orchestrator",
			objs: []client.Object{
				readyCluster(asyncWithOrchestrator), rolledOut(mysqlApp, stTestImage, 3),
				rolledOut(haproxyApp, stTestHAProxy, 1), rolledOut("orc", stTestOrc, 3, func(set *appsv1.StatefulSet) {
					set.Spec.Template = podSpec(orchestratorApp, stTestOrc)
				}),
			},
			converged:  true,
			available:  controller.Available,
			connection: true,
		},
		{
			name:       "ready without any workload created, as when the operator is simulated",
			objs:       []client.Object{readyCluster()},
			converged:  true,
			available:  controller.Available,
			connection: true,
		},
		{
			name: "the operator has not counted the new size yet",
			objs: []client.Object{readyCluster(func(cluster *psv1.PerconaServerMySQL) {
				cluster.Spec.MySQL.Size = 5
			})},
			message:    "Waiting for the operator to apply the latest configuration (MySQL: 5 pods)",
			available:  controller.Available,
			connection: true,
		},
		{
			name:       "the operator has not changed the image of the StatefulSet yet",
			objs:       []client.Object{readyCluster(), rolledOut(mysqlApp, stTestOldImage, 3)},
			message:    "Waiting for the operator to update StatefulSet my-ps-mysql to image " + stTestImage,
			available:  controller.Available,
			connection: true,
		},
		{
			name: "a StatefulSet has not picked up its pod template",
			objs: []client.Object{readyCluster(), rolledOut(mysqlApp, stTestImage, 3, func(set *appsv1.StatefulSet) {
				set.Generation = 3
			})},
			message:    "Waiting for StatefulSet my-ps-mysql to pick up its new pod template",
			available:  controller.Available,
			connection: true,
		},
		{
			name: "a new engine image rolls out",
			objs: []client.Object{
				readyCluster(), rolledOut(mysqlApp, stTestImage, 3, rolling),
				revision(t, "my-ps-mysql-1", stTestOldImage),
			},
			message:    "Waiting for StatefulSet my-ps-mysql to roll out (1 of 3 pods updated)",
			available:  controller.Available,
			operations: []string{corev1alpha1.ConditionUpgrading},
			connection: true,
		},
		{
			name: "a configuration change rolls out",
			objs: []client.Object{
				readyCluster(), rolledOut(mysqlApp, stTestImage, 3, rolling),
				revision(t, "my-ps-mysql-1", stTestImage),
			},
			message:    "Waiting for StatefulSet my-ps-mysql to roll out (1 of 3 pods updated)",
			available:  controller.Available,
			connection: true,
		},
		{
			name: "the MySQL Router Deployment is not available yet",
			objs: []client.Object{readyCluster(withRouter), routerDeployment(func(d *appsv1.Deployment) {
				d.Status.AvailableReplicas = 1
			})},
			message:    "Waiting for Deployment my-ps-router to become available (1 of 2 pods available)",
			available:  controller.Available,
			connection: true,
		},
		{
			name: "a StatefulSet of another cluster is not waited for",
			objs: []client.Object{readyCluster(), rolledOut(mysqlApp, stTestOldImage, 3, func(set *appsv1.StatefulSet) {
				set.OwnerReferences = controlledBy("other")
				set.OwnerReferences[0].UID = "other-uid"
			})},
			converged:  true,
			available:  controller.Available,
			connection: true,
		},
		{
			name:       "an operator error it has no name for",
			objs:       []client.Object{readyCluster(failing(reasonOperatorError, "reconcile: services: something broke"))},
			message:    "The operator reports an error: reconcile: services: something broke",
			available:  controller.Available,
			problems:   []string{corev1alpha1.ReasonEngineError},
			connection: true,
		},
		{
			name:       "MySQL rejects the operator's credentials",
			objs:       []client.Object{readyCluster(failing(reasonOperatorError, "reconcile: users: Error 1045: Access denied for user 'operator'@'10.0.0.1'"))},
			message:    "The operator reports an error: reconcile: users: Error 1045: Access denied for user 'operator'@'10.0.0.1'",
			available:  controller.Available,
			problems:   []string{reasonAuthenticationFailed},
			connection: true,
		},
		{
			name:       "the operator refuses the configuration",
			objs:       []client.Object{readyCluster(failing(reasonOperatorError, "check and set defaults for db/my-ps: MySQL size should be 3 or greater"))},
			message:    "The operator reports an error: check and set defaults for db/my-ps: MySQL size should be 3 or greater",
			available:  controller.Available,
			problems:   []string{reasonConfigurationRejected},
			connection: true,
		},
		{
			name:       "the certificates are not ready",
			objs:       []client.Object{readyCluster(failing(reasonOperatorError, "reconcile: TLS secret: timeout: tls certificate from certmanager is not ready: my-ps-ssl"))},
			message:    "The operator reports an error: reconcile: TLS secret: timeout: tls certificate from certmanager is not ready: my-ps-ssl",
			available:  controller.Available,
			problems:   []string{reasonTLSCertificatesNotReady},
			connection: true,
		},
		{
			name:       "growing the volumes exceeds the quota",
			objs:       []client.Object{readyCluster(failing(reasonOperatorError, "reconcile: persistent volumes: datadir-my-ps-mysql-0 pvc resize failed: ExceededQuota: PVC resize failed"))},
			message:    "The operator reports an error: reconcile: persistent volumes: datadir-my-ps-mysql-0 pvc resize failed: ExceededQuota: PVC resize failed",
			available:  controller.Available,
			problems:   []string{corev1alpha1.ReasonStorageQuotaExceeded},
			connection: true,
		},
		{
			name:       "the operator detects a full crash of the group",
			objs:       []client.Object{readyCluster(failing("FullClusterCrashDetected", "Full cluster crash detected"))},
			message:    "The operator reports an error: Full cluster crash detected",
			available:  controller.Available,
			problems:   []string{"FullClusterCrashDetected"},
			connection: true,
		},
		{
			name: "a conflicting write is a wait",
			objs: []client.Object{readyCluster(failing(reasonOperatorError,
				`reconcile: Operation cannot be fulfilled on perconaservermysqls.ps.percona.com "my-ps": the object has been modified`))},
			message:    "Waiting for the operator to retry a conflicting write",
			available:  controller.Available,
			connection: true,
		},
		{
			name:            "paused outside OpenEverest",
			objs:            []client.Object{readyCluster(paused)},
			message:         "The cluster is paused on its PerconaServerMySQL object",
			available:       controller.Unavailable,
			availableReason: reasonPaused,
			problems:        []string{reasonPausedOutsideOpenEverest},
			terminal:        true,
		},
		{
			name:            "paused for a restore through OpenEverest",
			objs:            []client.Object{readyCluster(paused), oeRestore("r1", backupv1alpha1.RestoreStateRunning)},
			message:         "Restore r1 is running",
			available:       controller.Unavailable,
			availableReason: reasonRestoring,
			operations:      []string{corev1alpha1.ConditionRestoring},
		},
		{
			name:            "paused for a restore created on the operator",
			objs:            []client.Object{readyCluster(paused), opRestore("direct", psv1.RestoreRunning, 100)},
			message:         "PerconaServerMySQLRestore direct is running",
			available:       controller.Unavailable,
			availableReason: reasonRestoring,
			operations:      []string{corev1alpha1.ConditionRestoring},
		},
		{
			name: "an error while a restore runs is not reported",
			objs: []client.Object{
				readyCluster(paused, failing(reasonOperatorError, "reconcile: something")),
				opRestore("direct", psv1.RestoreStarting, 100),
			},
			message:         "PerconaServerMySQLRestore direct is running",
			available:       controller.Unavailable,
			availableReason: reasonRestoring,
			operations:      []string{corev1alpha1.ConditionRestoring},
		},
		{
			name: "left paused by a failed restore",
			objs: []client.Object{
				readyCluster(paused), opRestore("old", psv1.RestoreSucceeded, 100), opRestore("latest", psv1.RestoreFailed, 200),
			},
			message:         "The cluster is paused after PerconaServerMySQLRestore latest failed",
			available:       controller.Unavailable,
			availableReason: reasonPaused,
			problems:        []string{reasonPausedAfterFailedRestore},
			terminal:        true,
		},
		{
			name: "paused after a restore that succeeded",
			objs: []client.Object{
				readyCluster(paused), opRestore("old", psv1.RestoreFailed, 100), opRestore("latest", psv1.RestoreSucceeded, 200),
			},
			message:         "The cluster is paused on its PerconaServerMySQL object",
			available:       controller.Unavailable,
			availableReason: reasonPaused,
			problems:        []string{reasonPausedOutsideOpenEverest},
			terminal:        true,
		},
		{
			name: "starting again after a pause",
			objs: []client.Object{readyCluster(paused, func(cluster *psv1.PerconaServerMySQL) {
				cluster.Spec.Pause = false
			})},
			message:         "Waiting for MySQL (0 of 3 ready); HAProxy (0 of 1 ready)",
			available:       controller.Unavailable,
			availableReason: reasonPaused,
		},
		{
			name: "the volumes of the MySQL pods are being resized",
			objs: []client.Object{readyCluster(func(cluster *psv1.PerconaServerMySQL) {
				cluster.Annotations = map[string]string{string(naming.AnnotationPVCResizeInProgress): "2026-11-19T10:00:00Z"}
			})},
			message:    "Waiting for the volumes of the MySQL pods to be resized to 20Gi",
			available:  controller.Available,
			operations: []string{corev1alpha1.ConditionStorageResizing},
			connection: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := tt.spec
			if spec.Components == nil {
				spec = statusInstance()
			}
			c := statusContext(t, spec, tt.objs...)

			st, err := clusterStatus(c)

			require.NoError(t, err)
			assert.Equal(t, tt.converged, st.Converged)
			assert.Equal(t, tt.message, st.Message)
			assert.Equal(t, tt.available, st.Available)
			assert.Equal(t, tt.availableReason, st.AvailableReason)
			assert.Equal(t, tt.problems, problemReasons(st))
			if tt.terminal {
				require.NotEmpty(t, st.Problems)
				assert.True(t, st.Problems[0].Terminal)
			} else {
				for _, p := range st.Problems {
					assert.False(t, p.Terminal, p.Reason)
				}
			}
			assert.Equal(t, tt.operations, operationTypes(st))
			if tt.connection {
				assert.Equal(t, "secret", st.ConnectionDetails.Password)
				assert.NotEmpty(t, st.ConnectionDetails.Host)
			} else {
				assert.Empty(t, st.ConnectionDetails.Host)
			}
		})
	}
}

func TestStatusUpgradingMessage(t *testing.T) {
	t.Parallel()

	c := statusContext(t, statusInstance(),
		readyCluster(), rolledOut(mysqlApp, stTestImage, 3, rolling),
		revision(t, "my-ps-mysql-1", stTestOldImage),
	)

	st, err := clusterStatus(c)

	require.NoError(t, err)
	require.Len(t, st.Operations, 1)
	assert.Equal(t, controller.Operation{
		Type:    corev1alpha1.ConditionUpgrading,
		Reason:  reasonRollingOutImage,
		Message: "Rolling out image " + stTestImage + " (the MySQL pods were running " + stTestOldImage + ")",
	}, st.Operations[0])
}

func TestStatusConnectionDetails(t *testing.T) {
	t.Parallel()

	t.Run("the users Secret is missing", func(t *testing.T) {
		t.Parallel()
		c := statusContextWithout(t, statusInstance(), readyCluster())

		_, err := clusterStatus(c)

		require.Error(t, err)
		assert.Contains(t, err.Error(), stTestSecrets)
	})

	t.Run("not published before clients can connect", func(t *testing.T) {
		t.Parallel()
		c := statusContextWithout(t, statusInstance(), readyCluster(clusterState(psv1.StateInitializing), mysqlStatus(func(s *psv1.StatefulAppStatus) {
			s.Ready = 0
		})))

		st, err := clusterStatus(c)

		require.NoError(t, err)
		assert.Empty(t, st.ConnectionDetails.Host)
	})

	t.Run("published as soon as clients can connect", func(t *testing.T) {
		t.Parallel()
		c := statusContext(t, statusInstance(), readyCluster(), rolledOut(mysqlApp, stTestOldImage, 3))

		st, err := clusterStatus(c)

		require.NoError(t, err)
		assert.False(t, st.Converged)
		assert.Equal(t, controller.ConnectionDetails{
			Type:     "mysql",
			Provider: common.ProviderName,
			Host:     stTestHost,
			Port:     "3306",
			Username: string(psv1.UserRoot),
			Password: "secret",
		}, st.ConnectionDetails)
	})
}

func TestEnqueueClusterWorkloadInstance(t *testing.T) {
	t.Parallel()

	enqueue := enqueueClusterWorkloadInstance()
	set := rolledOut(mysqlApp, stTestImage, 3)
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: stTestNamespace, Name: stTestName}}},
		enqueue(context.Background(), set))

	set.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "x", Controller: new(true)}}
	assert.Empty(t, enqueue(context.Background(), set))

	set.OwnerReferences = nil
	assert.Empty(t, enqueue(context.Background(), set))
}

func TestEnqueueOperatorRestoreInstance(t *testing.T) {
	t.Parallel()

	enqueue := enqueueOperatorRestoreInstance()
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: stTestNamespace, Name: stTestName}}},
		enqueue(context.Background(), opRestore("r", psv1.RestoreRunning, 1)))
	assert.Empty(t, enqueue(context.Background(), &psv1.PerconaServerMySQLRestore{}))
}
