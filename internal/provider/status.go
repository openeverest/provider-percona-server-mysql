package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// Reasons of the problems the provider reports. A problem's reason becomes
// the status word users read, so each names one cause.
const (
	// reasonPausedOutsideOpenEverest: spec.pause is set on the
	// PerconaServerMySQL object and no restore explains it. An Instance
	// cannot ask for that, so it was set there by hand.
	reasonPausedOutsideOpenEverest = "PausedOutsideOpenEverest"
	// reasonPausedAfterFailedRestore: the operator paused the cluster for a
	// restore that failed, and does not start it again by itself.
	reasonPausedAfterFailedRestore = "PausedAfterFailedRestore"
	// reasonAuthenticationFailed: MySQL rejects the credentials the
	// operator connects with.
	reasonAuthenticationFailed = "AuthenticationFailed"
	// reasonConfigurationRejected: the operator refuses the cluster's
	// configuration.
	reasonConfigurationRejected = "ConfigurationRejected"
	// reasonTLSCertificatesNotReady: the operator cannot get the cluster's
	// certificates.
	reasonTLSCertificatesNotReady = "TLSCertificatesNotReady"
)

// Reasons explaining why clients cannot connect.
const (
	reasonPaused       = "Paused"
	reasonRestoring    = corev1alpha1.ConditionRestoring
	reasonNoNodeReady  = "NoNodeReady"
	reasonNoProxyReady = "NoProxyReady"
)

// The names the operator gives the workloads it runs for a cluster, after the
// cluster's name, and their main containers.
const (
	mysqlApp        = "mysql"
	orchestratorApp = "orchestrator"
	haproxyApp      = "haproxy"
	routerApp       = "router"

	orchestratorWorkloadSuffix = "-orc"
)

// reasonRollingOutImage is the reason of the Upgrading operation.
const reasonRollingOutImage = "RollingOutImage"

// reasonOperatorError is the reason the operator gives its Error condition
// for a failed pass over the cluster. Any other reason names the cause.
const reasonOperatorError = "ErrorReconcile"

// The API server refuses a write made from a stale read with this message.
// The operator fails its pass on one and tries again on the next.
const errFragmentConflict = "Operation cannot be fulfilled"

// upstreamErrorReasons names the operator errors a user can act on, by a
// fragment of their message. The first match wins.
var upstreamErrorReasons = []struct{ fragment, reason string }{
	// MySQL's own message for rejected credentials, which the operator
	// passes on when the users Secret does not match the data.
	{"Access denied for user", reasonAuthenticationFailed},
	// The operator checks the PerconaServerMySQL before it acts on it.
	{"check and set defaults", reasonConfigurationRejected},
	{"failed to validate", reasonConfigurationRejected},
	{"is less than actual storage", reasonConfigurationRejected},
	{"TLS secret: ", reasonTLSCertificatesNotReady},
	{"tls certificate from certmanager", reasonTLSCertificatesNotReady},
	{"pvc resize failed: " + naming.EventExceededQuota, corev1alpha1.ReasonStorageQuotaExceeded},
	{"pvc resize failed", corev1alpha1.ReasonStorageResizeFailed},
}

// pauseCause says who paused the cluster, if anyone did.
type pauseCause int

const (
	notPaused pauseCause = iota
	// pausedForRestore: the operator stops the cluster while a restore
	// replaces its data, and starts it again once the restore succeeds.
	pausedForRestore
	// pausedAfterFailedRestore: the operator stopped the cluster for a
	// restore that failed, and leaves it stopped.
	pausedAfterFailedRestore
	// pausedOutside: nothing OpenEverest does will start the cluster again.
	pausedOutside
)

// restoreState is what the provider knows about the restores of the cluster.
type restoreState struct {
	// running says which restore runs, or is "" when none does.
	running string
	// lastFailed names the latest PerconaServerMySQLRestore of the cluster
	// when it failed.
	lastFailed string
}

// clusterPart is one of the workloads the operator runs for the cluster,
// with what the PerconaServerMySQL object asks of it.
type clusterPart struct {
	label  string
	status psv1.StatefulAppStatus
	// workload is the name of its StatefulSet, or of its Deployment when
	// deployment is set.
	workload   string
	deployment bool
	container  string
	image      string
	size       int32
}

// workloads are the StatefulSets and Deployments the operator runs for the
// cluster, by name.
type workloads struct {
	sets        map[string]*appsv1.StatefulSet
	deployments map[string]*appsv1.Deployment
}

// clusterStatus reports what the provider observes about the cluster: whether
// it runs what the Instance asks for, whether clients can connect, what is
// wrong and which long operation is running. The runtime derives the Instance's phase and conditions from these
// facts.
func clusterStatus(c *controller.Context) (controller.Status, error) {
	cluster, err := uncachedCluster(c)
	if apierrors.IsNotFound(err) {
		return controller.Progressing("Waiting for the PerconaServerMySQL object to be created"), nil
	}
	if err != nil {
		return controller.Status{}, err
	}

	restore, err := clusterRestores(c)
	if err != nil {
		return controller.Status{}, err
	}

	loads, err := clusterWorkloads(c, cluster)
	if err != nil {
		return controller.Status{}, err
	}

	st := observe(cluster, loads, restore)

	// The operator records which image the MySQL pods run as a digest, so an
	// engine image rolling out is read from the StatefulSet's revisions.
	if !st.Converged {
		from, err := rollingOutImage(c, loads.sets[cluster.Name+"-"+mysqlApp])
		if err != nil {
			return controller.Status{}, err
		}
		if from != "" {
			st = st.WithOperation(corev1alpha1.ConditionUpgrading, reasonRollingOutImage,
				fmt.Sprintf("Rolling out image %s (the MySQL pods were running %s)", cluster.Spec.MySQL.Image, from))
		}
	}

	// The connection details are reported as soon as clients can connect,
	// not only once the cluster has converged: whoever is told the database
	// is available must find where to connect, and the details must follow a
	// change that is still rolling out.
	if !st.Converged && (st.Available != controller.Available || cluster.Status.Host == "") {
		return st, nil
	}

	details, err := connectionDetails(c, cluster)
	if err != nil {
		// The users Secret is written by the operator, so its absence is
		// expected to pass. Returning the error has the runtime retry, which
		// nothing else would trigger: the provider does not watch Secrets.
		return controller.Status{}, fmt.Errorf("get connection details for PerconaServerMySQL %q: %w", c.Name(), err)
	}
	st.ConnectionDetails = details
	return st, nil
}

// uncachedCluster reads the PerconaServerMySQL object from the API server
// instead of the cache. Sync has just applied it, and the cache may still
// hold the object from before that, whose status would describe the previous
// spec. The client reads unstructured objects without the cache.
func uncachedCluster(c *controller.Context) (*psv1.PerconaServerMySQL, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(psv1.GroupVersion.WithKind("PerconaServerMySQL"))
	if err := c.Get(obj, c.Name()); err != nil {
		return nil, fmt.Errorf("get PerconaServerMySQL %q: %w", c.Name(), err)
	}

	cluster := &psv1.PerconaServerMySQL{}
	if err := fromUnstructured(obj, cluster); err != nil {
		return nil, fmt.Errorf("decode PerconaServerMySQL %q: %w", c.Name(), err)
	}
	return cluster, nil
}

func fromUnstructured(obj *unstructured.Unstructured, into any) error {
	raw, err := obj.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// clusterRestores returns the restore that is in progress for the Instance,
// if any, and whether the cluster's latest operator restore failed. A restore
// stops the cluster, replaces its data and starts it again, so the restore
// reaching a terminal state is what ends it, not what the engine reports
// meanwhile.
func clusterRestores(c *controller.Context) (restoreState, error) {
	active, err := activeRestores(c, c.Namespace(), c.Name())
	if err != nil {
		return restoreState{}, err
	}
	if len(active) > 0 {
		names := make([]string, 0, len(active))
		for i := range active {
			names = append(names, active[i].Name)
		}
		return restoreState{running: fmt.Sprintf("Restore %s is running", slices.Min(names))}, nil
	}

	// The operator also restores from a PerconaServerMySQLRestore created on
	// it directly, and stops the cluster for it the same way.
	list := &psv1.PerconaServerMySQLRestoreList{}
	if err := c.List(list); err != nil {
		return restoreState{}, fmt.Errorf("list PerconaServerMySQLRestore resources for cluster %q: %w", c.Name(), err)
	}
	var running []string
	var latest *psv1.PerconaServerMySQLRestore
	for i := range list.Items {
		r := &list.Items[i]
		if r.Spec.ClusterName != c.Name() || !r.DeletionTimestamp.IsZero() {
			continue
		}
		switch r.Status.State {
		case psv1.RestoreStarting, psv1.RestoreRunning:
			running = append(running, r.Name)
		}
		if latest == nil || latest.CreationTimestamp.Before(&r.CreationTimestamp) ||
			(latest.CreationTimestamp.Equal(&r.CreationTimestamp) && latest.Name < r.Name) {
			latest = r
		}
	}
	if len(running) > 0 {
		return restoreState{running: fmt.Sprintf("PerconaServerMySQLRestore %s is running", slices.Min(running))}, nil
	}
	if latest != nil && (latest.Status.State == psv1.RestoreFailed || latest.Status.State == psv1.RestoreError) {
		return restoreState{lastFailed: latest.Name}, nil
	}
	return restoreState{}, nil
}

// clusterWorkloads returns the StatefulSets and Deployments the operator runs
// for the cluster, matched to it by their controller reference.
func clusterWorkloads(c *controller.Context, cluster *psv1.PerconaServerMySQL) (workloads, error) {
	sets := &appsv1.StatefulSetList{}
	if err := c.List(sets); err != nil {
		return workloads{}, fmt.Errorf("list StatefulSets of cluster %q: %w", cluster.Name, err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := c.List(deployments); err != nil {
		return workloads{}, fmt.Errorf("list Deployments of cluster %q: %w", cluster.Name, err)
	}

	loads := workloads{sets: map[string]*appsv1.StatefulSet{}, deployments: map[string]*appsv1.Deployment{}}
	for i := range sets.Items {
		if set := &sets.Items[i]; metav1.IsControlledBy(set, cluster) && set.DeletionTimestamp.IsZero() {
			loads.sets[set.Name] = set
		}
	}
	for i := range deployments.Items {
		if d := &deployments.Items[i]; metav1.IsControlledBy(d, cluster) && d.DeletionTimestamp.IsZero() {
			loads.deployments[d.Name] = d
		}
	}
	return loads, nil
}

// observe turns the PerconaServerMySQL object, the workloads the operator
// runs for it and its restores into the facts the provider reports.
func observe(cluster *psv1.PerconaServerMySQL, loads workloads, restore restoreState) controller.Status {
	pause := pausedBy(cluster, restore)

	var st controller.Status
	st.Available, st.AvailableReason, st.AvailableMessage = availability(cluster, restore, pause)

	switch pause {
	case pausedOutside:
		// The provider never sets spec.pause and does not own the field, so
		// its Sync leaves it alone: only whoever set it can start the
		// database again.
		st = st.WithTerminalProblem(reasonPausedOutsideOpenEverest,
			"The cluster is paused: spec.pause is set on its PerconaServerMySQL object, outside OpenEverest. Unset it to start the database again.")
	case pausedAfterFailedRestore:
		st = st.WithTerminalProblem(reasonPausedAfterFailedRestore, fmt.Sprintf(
			"The cluster is paused: PerconaServerMySQLRestore %s failed, and the operator does not start the cluster again after a failed restore. Restore again, or unset spec.pause on its PerconaServerMySQL object.",
			restore.lastFailed))
	}

	// A restore stops the cluster, and the operator may fail passes while it
	// cannot reach it. Those errors come with the restore; one that outlives
	// it is reported once the restore has ended.
	if reason, message, failing := upstreamError(cluster); failing && restore.running == "" {
		// Never terminal, even for an error only a person can fix: the
		// operator does not say which version of the spec its error is
		// about, so right after a fix is applied the error still showing may
		// be the one being fixed.
		st = st.WithProblem(reason, message)
	}

	if restore.running != "" {
		st = st.WithOperation(corev1alpha1.ConditionRestoring, reasonRestoring, restore.running)
	}

	// The operator marks the object while it grows the volumes of the MySQL
	// pods, and removes the mark once they all have the requested size.
	if cluster.PVCResizeInProgress() {
		st = st.WithOperation(corev1alpha1.ConditionStorageResizing,
			corev1alpha1.ReasonStorageExpansionTriggered, "Resizing the volumes of the MySQL pods to "+requestedStorage(cluster))
	}

	st.Message = waitingFor(cluster, loads, restore, pause)
	st.Converged = st.Message == ""
	return st
}

// pausedBy says who paused the cluster. The operator pauses it itself to
// restore it, and starts it again once the restore succeeds; a pause no
// restore explains was set outside OpenEverest.
func pausedBy(cluster *psv1.PerconaServerMySQL, restore restoreState) pauseCause {
	switch {
	case !cluster.Spec.Pause:
		return notPaused
	case restore.running != "":
		return pausedForRestore
	case restore.lastFailed != "":
		return pausedAfterFailedRestore
	default:
		return pausedOutside
	}
}

// upstreamError returns the error the operator reports for its last pass
// over the cluster, with a reason for it, and whether it counts as a failure.
// The operator records the error of a failed pass in its Error condition, and
// one it found on the cluster itself, a full crash of the group, with a
// reason of its own.
func upstreamError(cluster *psv1.PerconaServerMySQL) (string, string, bool) {
	if cluster.Status.State != psv1.StateError {
		return "", "", false
	}
	cond := meta.FindStatusCondition(cluster.Status.Conditions, psv1.StateError.String())
	message := ""
	if cond != nil {
		message = strings.TrimSpace(cond.Message)
	}
	if message == "" {
		message = "The operator reports an error without a message"
	}
	if strings.Contains(message, errFragmentConflict) {
		return "", message, false
	}
	if cond != nil && cond.Reason != "" && cond.Reason != reasonOperatorError {
		return cond.Reason, message, true
	}
	return upstreamErrorReason(message), message, true
}

// upstreamErrorReason picks the reason for an operator error from its message.
func upstreamErrorReason(message string) string {
	for _, known := range upstreamErrorReasons {
		if strings.Contains(message, known.fragment) {
			return known.reason
		}
	}
	return corev1alpha1.ReasonEngineError
}

// waitingFor says what keeps the cluster from running what the Instance asks
// for, in the operator's own words where it has any. It returns "" when
// nothing does: the operator reports the cluster ready for the sizes the
// object asks for, and every workload it runs for the cluster runs the
// object's images and sizes and has rolled out.
func waitingFor(cluster *psv1.PerconaServerMySQL, loads workloads, restore restoreState, pause pauseCause) string {
	state := cluster.Status.State
	switch {
	case pause == pausedOutside:
		return "The cluster is paused on its PerconaServerMySQL object"
	case pause == pausedAfterFailedRestore:
		return fmt.Sprintf("The cluster is paused after PerconaServerMySQLRestore %s failed", restore.lastFailed)
	case restore.running != "":
		return restore.running
	case state == psv1.StateError:
		_, message, failing := upstreamError(cluster)
		if !failing {
			return "Waiting for the operator to retry a conflicting write"
		}
		return "The operator reports an error: " + message
	case state == "":
		return "Waiting for the operator to start creating the cluster"
	case cluster.PVCResizeInProgress():
		return "Waiting for the volumes of the MySQL pods to be resized to " + requestedStorage(cluster)
	}

	// The operator records the sizes it counted pods against on every pass,
	// so sizes other than the object's mean it has not acted on the latest
	// spec yet.
	parts := clusterParts(cluster)
	for _, part := range parts {
		if part.status.Size != part.size {
			return fmt.Sprintf("Waiting for the operator to apply the latest configuration (%s: %d pods)", part.label, part.size)
		}
	}
	if state != psv1.StateReady {
		return notReadyWait(cluster, parts, loads)
	}

	// The operator reports the cluster ready from its pods, and right after
	// a change to the object its status still describes the previous spec,
	// so the workloads must also run what the object asks for.
	if wait := workloadsWait(parts, loads); wait != "" {
		return "Waiting for " + wait
	}
	return ""
}

// clusterParts returns the workloads the operator runs for the cluster: the
// MySQL pods, the orchestrator of an asynchronous cluster and the proxy.
func clusterParts(cluster *psv1.PerconaServerMySQL) []clusterPart {
	parts := []clusterPart{{
		label:     "MySQL",
		status:    cluster.Status.MySQL,
		workload:  cluster.Name + "-" + mysqlApp,
		container: mysqlApp,
		image:     cluster.Spec.MySQL.Image,
		size:      cluster.Spec.MySQL.Size,
	}}
	if cluster.Spec.MySQL.IsAsync() && cluster.OrchestratorEnabled() {
		parts = append(parts, clusterPart{
			label:     "Orchestrator",
			status:    cluster.Status.Orchestrator,
			workload:  cluster.Name + orchestratorWorkloadSuffix,
			container: orchestratorApp,
			image:     cluster.Spec.Orchestrator.Image,
			size:      cluster.Spec.Orchestrator.Size,
		})
	}
	if cluster.HAProxyEnabled() {
		parts = append(parts, clusterPart{
			label:     "HAProxy",
			status:    cluster.Status.HAProxy,
			workload:  cluster.Name + "-" + haproxyApp,
			container: haproxyApp,
			image:     cluster.Spec.Proxy.HAProxy.Image,
			size:      cluster.Spec.Proxy.HAProxy.Size,
		})
	}
	if cluster.RouterEnabled() {
		parts = append(parts, clusterPart{
			label:      "MySQL Router",
			status:     cluster.Status.Router,
			workload:   cluster.Name + "-" + routerApp,
			deployment: true,
			container:  routerApp,
			image:      cluster.Spec.Proxy.Router.Image,
			size:       cluster.Spec.Proxy.Router.Size,
		})
	}
	return parts
}

// proxyPart returns the proxy the cluster runs, if any.
func proxyPart(parts []clusterPart) (clusterPart, bool) {
	for _, part := range parts {
		if part.container == haproxyApp || part.container == routerApp {
			return part, true
		}
	}
	return clusterPart{}, false
}

// notReadyWait says what a cluster the operator does not report ready is
// waiting for: the pods the operator does not count as ready, then a
// workload that has not rolled out, then the operator itself.
func notReadyWait(cluster *psv1.PerconaServerMySQL, parts []clusterPart, loads workloads) string {
	var counts []string
	for _, part := range parts {
		if part.status.Ready < part.size {
			counts = append(counts, fmt.Sprintf("%s (%d of %d ready)", part.label, part.status.Ready, part.size))
		}
	}
	if len(counts) > 0 {
		return "Waiting for " + strings.Join(counts, "; ")
	}
	if wait := workloadsWait(parts, loads); wait != "" {
		return "Waiting for " + wait
	}
	// Every pod is ready, but the operator waits for more: the members to
	// form the group or replicate, or a load balancer to get an address.
	return fmt.Sprintf("Waiting for the operator to report the cluster ready (it reports %s)", string(cluster.Status.State))
}

// workloadsWait returns what the first workload that does not run what the
// object asks for is waiting for, or "" when all do. A workload the operator
// has not created is not waited for: the operator does not report a cluster
// ready without its pods.
func workloadsWait(parts []clusterPart, loads workloads) string {
	for _, part := range parts {
		var wait string
		if part.deployment {
			if d, ok := loads.deployments[part.workload]; ok {
				wait = deploymentWait(part, d)
			}
		} else if set, ok := loads.sets[part.workload]; ok {
			wait = statefulSetWait(part, set)
		}
		if wait != "" {
			return wait
		}
	}
	return ""
}

func statefulSetWait(part clusterPart, set *appsv1.StatefulSet) string {
	st := set.Status
	switch {
	case st.ObservedGeneration < set.Generation:
		return fmt.Sprintf("StatefulSet %s to pick up its new pod template", set.Name)
	case part.image != "" && containerImage(set.Spec.Template.Spec, part.container) != part.image:
		return fmt.Sprintf("the operator to update StatefulSet %s to image %s", set.Name, part.image)
	case replicas(set.Spec.Replicas) != part.size:
		return fmt.Sprintf("the operator to scale StatefulSet %s to %d pods", set.Name, part.size)
	case st.UpdatedReplicas < st.Replicas:
		return fmt.Sprintf("StatefulSet %s to roll out (%d of %d pods updated)", set.Name, st.UpdatedReplicas, st.Replicas)
	case st.Replicas != part.size:
		return fmt.Sprintf("StatefulSet %s to scale to %d pods (it has %d)", set.Name, part.size, st.Replicas)
	case st.ReadyReplicas < st.Replicas:
		return fmt.Sprintf("StatefulSet %s to become ready (%d of %d pods ready)", set.Name, st.ReadyReplicas, st.Replicas)
	}
	return ""
}

func deploymentWait(part clusterPart, d *appsv1.Deployment) string {
	st := d.Status
	switch {
	case st.ObservedGeneration < d.Generation:
		return fmt.Sprintf("Deployment %s to pick up its new pod template", d.Name)
	case part.image != "" && containerImage(d.Spec.Template.Spec, part.container) != part.image:
		return fmt.Sprintf("the operator to update Deployment %s to image %s", d.Name, part.image)
	case replicas(d.Spec.Replicas) != part.size:
		return fmt.Sprintf("the operator to scale Deployment %s to %d pods", d.Name, part.size)
	case st.UpdatedReplicas < st.Replicas:
		return fmt.Sprintf("Deployment %s to roll out (%d of %d pods updated)", d.Name, st.UpdatedReplicas, st.Replicas)
	case st.AvailableReplicas < part.size:
		return fmt.Sprintf("Deployment %s to become available (%d of %d pods available)", d.Name, st.AvailableReplicas, part.size)
	}
	return ""
}

// replicas returns the replicas a workload asks for: one when unset.
func replicas(n *int32) int32 {
	if n == nil {
		return 1
	}
	return *n
}

// containerImage returns the image of the named container of a pod spec.
func containerImage(spec corev1.PodSpec, name string) string {
	for _, c := range spec.Containers {
		if c.Name == name {
			return c.Image
		}
	}
	return ""
}

// availability says whether clients can connect: they reach the database
// through the proxy, which needs a MySQL pod to send them to, so both need a
// ready pod. Without a proxy the MySQL pods alone count.
//
// It reads the ready pods the operator counted on its last pass over the
// cluster.
func availability(cluster *psv1.PerconaServerMySQL, restore restoreState, pause pauseCause) (controller.Availability, string, string) {
	state := cluster.Status.State
	switch {
	case restore.running != "":
		return controller.Unavailable, reasonRestoring, "A restore is replacing the data"
	case pause != notPaused:
		return controller.Unavailable, reasonPaused, "The cluster is paused"
	case state == psv1.StatePaused, state == psv1.StateStopping:
		return controller.Unavailable, reasonPaused, "The cluster is starting again after a pause"
	case state == "":
		// The operator has not looked at the cluster yet. The runtime knows
		// whether it has ever been available.
		return controller.AvailabilityUnreported, "", ""
	}

	parts := clusterParts(cluster)
	if parts[0].status.Ready < 1 {
		return controller.Unavailable, reasonNoNodeReady, "No MySQL pod is ready"
	}
	if proxy, ok := proxyPart(parts); ok && proxy.status.Ready < 1 {
		return controller.Unavailable, reasonNoProxyReady, fmt.Sprintf("No %s pod is ready", proxy.label)
	}
	return controller.Available, "", ""
}

// rollingOutImage returns the image the MySQL pods ran before the pod
// template the StatefulSet is rolling out, when that template changes the
// image, or "". The StatefulSet controller keeps each pod template it rolled
// out in a ControllerRevision, and moves currentRevision to updateRevision
// once every pod runs the latest one.
func rollingOutImage(c *controller.Context, set *appsv1.StatefulSet) (string, error) {
	if set == nil || set.Status.CurrentRevision == "" || set.Status.CurrentRevision == set.Status.UpdateRevision {
		return "", nil
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ControllerRevision"))
	if err := c.Get(obj, set.Status.CurrentRevision); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get ControllerRevision %q of StatefulSet %q: %w", set.Status.CurrentRevision, set.Name, err)
	}
	rev := &appsv1.ControllerRevision{}
	if err := fromUnstructured(obj, rev); err != nil {
		return "", fmt.Errorf("decode ControllerRevision %q: %w", set.Status.CurrentRevision, err)
	}
	var data struct {
		Spec struct {
			Template corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(rev.Data.Raw, &data); err != nil {
		return "", fmt.Errorf("decode the pod template of ControllerRevision %q: %w", rev.Name, err)
	}

	from := containerImage(data.Spec.Template.Spec, mysqlApp)
	if from == "" || from == containerImage(set.Spec.Template.Spec, mysqlApp) {
		return "", nil
	}
	return from, nil
}

// requestedStorage returns the volume size the PerconaServerMySQL asks for
// the MySQL pods.
func requestedStorage(cluster *psv1.PerconaServerMySQL) string {
	vs := cluster.Spec.MySQL.VolumeSpec
	if vs == nil || vs.PersistentVolumeClaim == nil {
		return "the requested size"
	}
	size, ok := vs.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	if !ok {
		return "the requested size"
	}
	return size.String()
}

// enqueueClusterWorkloadInstance maps a StatefulSet or Deployment the
// operator runs for a cluster to the Instance of that cluster, by its
// controller reference. Whether the Instance has converged depends on them
// having rolled out, and the operator's own status may not change again once
// they have.
func enqueueClusterWorkloadInstance() func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		owner := metav1.GetControllerOf(obj)
		if owner == nil || owner.Kind != "PerconaServerMySQL" {
			return nil
		}
		if gv, err := schema.ParseGroupVersion(owner.APIVersion); err != nil || gv.Group != psv1.GroupVersion.Group {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: owner.Name},
		}}
	}
}

// enqueueOperatorRestoreInstance maps a PerconaServerMySQLRestore to the
// Instance of the cluster it restores, so a restore created on the operator
// directly is reported as it starts and ends.
func enqueueOperatorRestoreInstance() func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		r, ok := obj.(*psv1.PerconaServerMySQLRestore)
		if !ok || r.Spec.ClusterName == "" {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: r.Spec.ClusterName},
		}}
	}
}
