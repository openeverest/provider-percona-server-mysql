// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mysql/definition"
)

const (
	certManagerGroup = "cert-manager.io"

	// caKey is the key holding the CA certificate in the certificate
	// secret, and in the connection secret.
	caKey = "ca.crt"

	// requireSecureTransportKey is the my.cnf variable that rejects client
	// connections without TLS.
	requireSecureTransportKey = "require_secure_transport"

	// tlsNamesTokenPrefix starts the token approving a change to the
	// additional names of a running instance's certificate, followed by a
	// hash of the names it leads to.
	tlsNamesTokenPrefix = "tls-names-"

	// externalIssuersIssue tracks external issuer support in the PS
	// operator.
	externalIssuersIssue = "https://perconadev.atlassian.net/browse/K8SPS-989"
)

// certificateGVK is the cert-manager Certificate the operator creates for the
// certificate secret when it issues it through cert-manager.
var certificateGVK = schema.GroupVersionKind{Group: certManagerGroup, Version: "v1", Kind: "Certificate"}

// certificateSecretKeys are the keys the certificate secret must hold,
// whether the user supplies it or the operator (or cert-manager) generates
// it.
var certificateSecretKeys = []string{"tls.crt", "tls.key", caKey}

// issuerKinds are the cert-manager issuer kinds an instance can name.
var issuerKinds = []definition.IssuerKind{definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer}

// tlsModes are the TLS modes the engine supports. The PS operator always
// encrypts traffic, so there is no disabled mode.
var tlsModes = []definition.TLSMode{definition.TLSModePrefer, definition.TLSModeRequire}

// tlsParameters returns the instance's TLS parameters.
func tlsParameters(c *controller.Context) (definition.TLSParameters, error) {
	return instanceTLSParameters(c.Instance())
}

// instanceTLSParameters returns the TLS parameters of in, with defaults
// applied.
func instanceTLSParameters(in *corev1alpha1.Instance) (definition.TLSParameters, error) {
	params := definition.Parameters{}
	if raw := in.Spec.Parameters; raw != nil && len(raw.Raw) > 0 {
		if err := json.Unmarshal(raw.Raw, &params); err != nil {
			return definition.TLSParameters{}, fmt.Errorf("decode parameters: %w", err)
		}
	}

	tls := definition.TLSParameters{}
	if params.TLS != nil {
		tls = *params.TLS
	}
	if tls.Mode == "" {
		tls.Mode = definition.TLSModePrefer
	}
	if tls.Issuer != nil && tls.Issuer.Name == "" {
		tls.Issuer = nil
	}
	if tls.Issuer != nil && tls.Issuer.Kind == "" {
		tls.Issuer.Kind = definition.IssuerKindIssuer
	}
	if tls.Issuer != nil && tls.Issuer.Group == "" {
		tls.Issuer.Group = certManagerGroup
	}

	return tls, nil
}

// certificateSecretName is the secret holding the certificate the instance
// presents to clients and other members; the operator generates it under
// its default name unless the instance brings its own.
func certificateSecretName(instanceName string, tls definition.TLSParameters) string {
	if tls.CertificateSecretName != "" {
		return tls.CertificateSecretName
	}
	return instanceName + "-ssl"
}

// operatorCertificateName is the cert-manager Certificate the operator
// creates for the certificate secret.
func operatorCertificateName(instanceName string) string {
	return instanceName + "-ssl"
}

// issuerSource is the group, kind and name of the issuer that signs the
// certificate, or "" when there is none.
func issuerSource(tls definition.TLSParameters) string {
	if tls.Issuer == nil {
		return ""
	}
	return issuerGroup(tls.Issuer.Group) + "/" + string(tls.Issuer.Kind) + "/" + tls.Issuer.Name
}

// issuerGroup returns the issuer API group, defaulting to cert-manager.io as
// cert-manager itself does.
func issuerGroup(group string) string {
	if group == "" {
		return certManagerGroup
	}
	return group
}

// configureTLS points the PS cluster at the certificate secret and, when set,
// the cert-manager issuer that signs it and the additional names. The PS
// operator always encrypts these connections: it manages the certificate
// itself (via cert-manager when installed, or a self-signed CA otherwise)
// unless the secret already exists and was not created by the operator, in
// which case it leaves it alone.
func configureTLS(c *controller.Context, cluster *psv1.PerconaServerMySQL, tls definition.TLSParameters) {
	cluster.Spec.SSLSecretName = certificateSecretName(c.Name(), tls)

	issuer := issuerRef(tls)
	if issuer == nil && len(tls.AdditionalNames) == 0 {
		return
	}
	cluster.Spec.TLS = &psv1.TLSSpec{
		IssuerConf: issuer,
		SANs:       slices.Clone(tls.AdditionalNames),
	}
}

// applyTLSMode sets require_secure_transport=ON in the engine configuration
// when TLS is required, keeping a value the user set explicitly. It runs
// after the engine configuration is rendered.
func applyTLSMode(cluster *psv1.PerconaServerMySQL, tls definition.TLSParameters) {
	if tls.Mode != definition.TLSModeRequire {
		return
	}
	if _, ok := configurationValue(cluster.Spec.MySQL.Configuration, requireSecureTransportKey); ok {
		return
	}
	line := requireSecureTransportKey + "=ON"
	if cluster.Spec.MySQL.Configuration == "" {
		cluster.Spec.MySQL.Configuration = line
		return
	}
	cluster.Spec.MySQL.Configuration += "\n" + line
}

// requiresSecureTransport reports whether the engine configuration rejects
// connections without TLS, so the provider's own clients must use it.
func requiresSecureTransport(cluster *psv1.PerconaServerMySQL) bool {
	value, ok := configurationValue(cluster.Spec.MySQL.Configuration, requireSecureTransportKey)
	if !ok {
		return false
	}
	switch strings.ToLower(value) {
	case "on", "1", "true":
		return true
	}
	return false
}

// configurationValue returns the value of key in a key=value my.cnf snippet.
// MySQL treats - and _ in option names alike.
func configurationValue(configuration, key string) (string, bool) {
	normalize := func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "-", "_") }
	for _, line := range strings.Split(configuration, "\n") {
		k, v, found := strings.Cut(line, "=")
		if !found || normalize(k) != normalize(key) {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"'`), true
	}
	return "", false
}

// appliedTLSNames are the additional names a PS cluster's certificate has
// been issued with.
func appliedTLSNames(cluster *psv1.PerconaServerMySQL) []string {
	if cluster.Spec.TLS == nil {
		return nil
	}
	return cluster.Spec.TLS.SANs
}

func setTLSNames(cluster *psv1.PerconaServerMySQL, names []string) {
	if len(names) == 0 {
		if cluster.Spec.TLS != nil {
			cluster.Spec.TLS.SANs = nil
			if cluster.Spec.TLS.IssuerConf == nil {
				cluster.Spec.TLS = nil
			}
		}
		return
	}
	if cluster.Spec.TLS == nil {
		cluster.Spec.TLS = &psv1.TLSSpec{}
	}
	cluster.Spec.TLS.SANs = slices.Clone(names)
}

// gateTLSNames holds back a change to the additional names of a running
// instance until approved: the certificate is reissued and the operator
// restarts the pods to load it. A new instance has its certificate issued
// with the names, without approval. On hold, cluster keeps the names
// currently applied. Once approved, the provider updates the names on the
// cert-manager Certificate, because the operator leaves a certificate from
// a user's issuer alone after it is created.
func gateTLSNames(c *controller.Context, cluster *psv1.PerconaServerMySQL) error {
	current := &psv1.PerconaServerMySQL{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching current PerconaServerMySQL: %w", err)
	}

	applied := appliedTLSNames(current)
	wanted := appliedTLSNames(cluster)
	if slices.Equal(applied, wanted) {
		return nil
	}

	description := "Reissue the certificate without additional names (rolling restart)"
	if len(wanted) > 0 {
		description = fmt.Sprintf("Reissue the certificate for the additional names %s (rolling restart)",
			strings.Join(wanted, ", "))
	}
	if !c.RequestMaintenance(tlsNamesToken(wanted), description, controller.MaintenanceRollingRestart) {
		setTLSNames(cluster, applied)
		return nil
	}

	return reissueCertificate(c, applied, wanted)
}

// tlsNamesToken identifies a change to the additional names by the names it
// leads to.
func tlsNamesToken(names []string) string {
	sum := sha256.Sum256([]byte(strings.Join(names, ",")))
	return tlsNamesTokenPrefix + hex.EncodeToString(sum[:])[:10]
}

// reissueCertificate replaces the applied additional names with the wanted
// ones on the cert-manager Certificate the operator created for the
// instance. cert-manager then reissues it from the same issuer, so the CA
// members and clients trust is unchanged.
func reissueCertificate(c *controller.Context, applied, wanted []string) error {
	name := operatorCertificateName(c.Name())
	cert, err := getCertificate(c, name)
	if err != nil || cert == nil {
		return err
	}

	dnsNames, _, err := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	if err != nil {
		return fmt.Errorf("read the names of Certificate %s: %w", name, err)
	}
	names := replaceNames(dnsNames, applied, wanted)
	if slices.Equal(names, dnsNames) {
		return nil
	}

	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"dnsNames": names}})
	if err != nil {
		return fmt.Errorf("encode the names of Certificate %s: %w", name, err)
	}
	log.FromContext(c.Context()).Info("Reissuing the certificate for its additional names",
		"certificate", name, "additionalNames", wanted)
	if err := c.Client().Patch(c.Context(), cert, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("update the names of Certificate %s: %w", name, err)
	}
	return nil
}

// replaceNames drops the applied names from names and appends the wanted
// ones it lacks, keeping the order of the rest.
func replaceNames(names, applied, wanted []string) []string {
	out := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return slices.Contains(applied, n) })
	for _, n := range wanted {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// getCertificate returns the named cert-manager Certificate, or nil when it
// doesn't exist or cert-manager isn't installed.
func getCertificate(c *controller.Context, name string) (*unstructured.Unstructured, error) {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	if err := c.Get(cert, name); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Certificate %s: %w", name, err)
	}
	return cert, nil
}

// validateTLS checks the TLS parameters.
func validateTLS(in *corev1alpha1.Instance) field.ErrorList {
	tls, err := instanceTLSParameters(in)
	if err != nil {
		return field.ErrorList{field.InternalError(field.NewPath("spec", "parameters"), err)}
	}
	path := field.NewPath("spec", "parameters", "tls")

	var errs field.ErrorList
	if !slices.Contains(tlsModes, tls.Mode) {
		errs = append(errs, field.NotSupported(path.Child("mode"), tls.Mode, tlsModes))
	}
	if tls.Issuer != nil {
		if group := issuerGroup(tls.Issuer.Group); group != certManagerGroup {
			errs = append(errs, field.Invalid(path.Child("issuer", "group"), group,
				"only "+certManagerGroup+" is supported: the PS operator can't use external issuers yet, see "+externalIssuersIssue))
		}
		if !slices.Contains(issuerKinds, tls.Issuer.Kind) {
			errs = append(errs, field.NotSupported(path.Child("issuer", "kind"), tls.Issuer.Kind, issuerKinds))
		}
	}
	if tls.CertificateSecretName != "" && tls.Issuer != nil {
		errs = append(errs, field.Forbidden(path.Child("issuer"), "use either a certificate secret or an issuer, not both"))
	}
	errs = append(errs, validateAdditionalNames(tls, path.Child("additionalNames"))...)

	return errs
}

func validateAdditionalNames(tls definition.TLSParameters, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if len(tls.AdditionalNames) > 0 && tls.CertificateSecretName != "" {
		errs = append(errs, field.Forbidden(path,
			"cannot be combined with certificateSecretName: that certificate carries its own names"))
	}
	seen := make(map[string]bool, len(tls.AdditionalNames))
	for i, name := range tls.AdditionalNames {
		switch {
		case net.ParseIP(name) != nil:
			errs = append(errs, field.Invalid(path.Index(i), name, "must be a DNS name, not an IP address"))
		case seen[name]:
			errs = append(errs, field.Duplicate(path.Index(i), name))
		default:
			if msgs := validation.IsDNS1123Subdomain(strings.TrimPrefix(name, "*.")); len(msgs) > 0 {
				errs = append(errs, field.Invalid(path.Index(i), name, "must be a DNS name: "+strings.Join(msgs, "; ")))
			}
		}
		seen[name] = true
	}
	return errs
}

// validateTLSTransition rejects a certificate source change on a provisioned
// instance: a new CA would be trusted by neither the existing members nor
// the clients still holding the old one.
func validateTLSTransition(in, old *corev1alpha1.Instance) field.ErrorList {
	if old == nil || old.Status.Phase == "" || old.Status.Phase == corev1alpha1.InstancePhasePending {
		return nil
	}
	before, err := instanceTLSParameters(old)
	if err != nil {
		return nil
	}
	after, err := instanceTLSParameters(in)
	if err != nil {
		return nil
	}

	path := field.NewPath("spec", "parameters", "tls")
	var errs field.ErrorList
	if certificateSecretName(old.Name, before) != certificateSecretName(in.Name, after) {
		errs = append(errs, field.Forbidden(path.Child("certificateSecretName"), "can't change on an existing instance"))
	}
	if issuerSource(before) != issuerSource(after) {
		errs = append(errs, field.Forbidden(path.Child("issuer"), "can't change on an existing instance"))
	}
	return errs
}

// validateTLSReferences checks the certificate Secret, and that the
// certificate source of the running PerconaServerMySQL doesn't change.
func validateTLSReferences(c *controller.Context) (field.ErrorList, error) {
	tls, err := tlsParameters(c)
	if err != nil {
		return nil, err
	}

	var errs field.ErrorList
	if tls.CertificateSecretName != "" {
		secretErrs, err := validateCertificateSecret(c, tls.CertificateSecretName)
		if err != nil {
			return nil, err
		}
		errs = append(errs, secretErrs...)
	}

	changeErrs, err := validateTLSChange(c, tls)
	if err != nil {
		return nil, err
	}
	return append(errs, changeErrs...), nil
}

func validateCertificateSecret(c *controller.Context, name string) (field.ErrorList, error) {
	path := field.NewPath("spec", "parameters", "tls", "certificateSecretName")

	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		if apierrors.IsNotFound(err) {
			return field.ErrorList{field.NotFound(path, name)}, nil
		}
		return nil, fmt.Errorf("get certificate secret %q: %w", name, err)
	}
	var missing []string
	for _, key := range certificateSecretKeys {
		if len(secret.Data[key]) == 0 {
			missing = append(missing, strconv.Quote(key))
		}
	}
	if len(missing) > 0 {
		return field.ErrorList{field.Invalid(path, name, "missing "+strings.Join(missing, ", "))}, nil
	}
	return nil, nil
}

// validateTLSChange rejects a certificate source that differs from the one
// the running PerconaServerMySQL uses, and new additional names on a
// certificate the operator generated without cert-manager: it would issue
// them from a new CA.
func validateTLSChange(c *controller.Context, tls definition.TLSParameters) (field.ErrorList, error) {
	current := &psv1.PerconaServerMySQL{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching current PerconaServerMySQL: %w", err)
	}

	currentIssuer := ""
	if current.Spec.TLS != nil && current.Spec.TLS.IssuerConf != nil {
		ref := current.Spec.TLS.IssuerConf
		currentIssuer = issuerGroup(ref.Group) + "/" + ref.Kind + "/" + ref.Name
	}

	path := field.NewPath("spec", "parameters", "tls")
	var errs field.ErrorList
	if current.Spec.SSLSecretName != "" && current.Spec.SSLSecretName != certificateSecretName(c.Name(), tls) {
		errs = append(errs, field.Forbidden(path.Child("certificateSecretName"), "can't change on an existing instance"))
	}
	if currentIssuer != issuerSource(tls) {
		errs = append(errs, field.Forbidden(path.Child("issuer"), "can't change on an existing instance"))
	}
	if len(errs) > 0 || slices.Equal(appliedTLSNames(current), tls.AdditionalNames) {
		return errs, nil
	}

	cert, err := getCertificate(c, operatorCertificateName(c.Name()))
	if err != nil {
		return nil, err
	}
	if cert == nil {
		errs = append(errs, field.Forbidden(path.Child("additionalNames"),
			"can't change on this instance: its certificate comes from a CA the operator generated without "+
				"cert-manager, and a new one would come from a new CA its members and clients don't trust"))
	}
	return errs, nil
}

// tlsConnectionCA returns the CA clients verify the instance's certificate
// with, or nil when it hasn't been issued yet.
func tlsConnectionCA(c *controller.Context, cluster *psv1.PerconaServerMySQL) ([]byte, error) {
	name := cluster.Spec.SSLSecretName
	if name == "" {
		return nil, nil
	}

	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get certificate secret %q: %w", name, err)
	}

	return secret.Data[caKey], nil
}

// issuerRef builds the cert-manager issuer reference the PS operator expects
// from the instance's TLS parameters, or nil to let the operator manage the
// certificate itself.
func issuerRef(tls definition.TLSParameters) *cmmeta.IssuerReference {
	if tls.Issuer == nil {
		return nil
	}
	return &cmmeta.IssuerReference{
		Name:  tls.Issuer.Name,
		Kind:  string(tls.Issuer.Kind),
		Group: issuerGroup(tls.Issuer.Group),
	}
}
