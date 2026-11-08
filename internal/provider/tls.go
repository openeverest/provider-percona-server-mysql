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
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mysql/definition"
)

const (
	certManagerGroup = "cert-manager.io"

	// caKey is the key holding the CA certificate in the certificate
	// secret, and in the connection secret.
	caKey = "ca.crt"
)

// certificateSecretKeys are the keys the certificate secret must hold,
// whether the user supplies it or the operator (or cert-manager) generates
// it.
var certificateSecretKeys = []string{"tls.crt", "tls.key", caKey}

// issuerKinds are the cert-manager issuer kinds an instance can name.
var issuerKinds = []definition.IssuerKind{definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer}

// tlsParameters returns the instance's TLS parameters.
func tlsParameters(c *controller.Context) (definition.TLSParameters, error) {
	return instanceTLSParameters(c.Instance())
}

// instanceTLSParameters returns the TLS parameters of in.
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
	if tls.Issuer != nil && tls.Issuer.Name == "" {
		tls.Issuer = nil
	}
	if tls.Issuer != nil && tls.Issuer.Kind == "" {
		tls.Issuer.Kind = definition.IssuerKindIssuer
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

// issuerSource is the kind and name of the issuer that signs the
// certificate, or "/" when there is none.
func issuerSource(tls definition.TLSParameters) string {
	if tls.Issuer == nil {
		return "/"
	}
	return string(tls.Issuer.Kind) + "/" + tls.Issuer.Name
}

// configureTLS points the PS cluster at the certificate secret and, when set,
// the cert-manager issuer that signs it. The PS operator always encrypts
// these connections: it manages the certificate itself (via cert-manager
// when installed, or a self-signed CA otherwise) unless the secret already
// exists and was not created by the operator, in which case it leaves it
// alone.
func configureTLS(c *controller.Context, cluster *psv1.PerconaServerMySQL, tls definition.TLSParameters) {
	cluster.Spec.SSLSecretName = certificateSecretName(c.Name(), tls)
	if issuer := issuerRef(tls); issuer != nil {
		cluster.Spec.TLS = &psv1.TLSSpec{IssuerConf: issuer}
	}
}

// validateTLS checks the TLS parameters.
func validateTLS(in *corev1alpha1.Instance) field.ErrorList {
	tls, err := instanceTLSParameters(in)
	if err != nil {
		return field.ErrorList{field.InternalError(field.NewPath("spec", "parameters"), err)}
	}
	path := field.NewPath("spec", "parameters", "tls")

	var errs field.ErrorList
	if tls.Issuer != nil && !slices.Contains(issuerKinds, tls.Issuer.Kind) {
		errs = append(errs, field.NotSupported(path.Child("issuer", "kind"), tls.Issuer.Kind, issuerKinds))
	}
	if tls.CertificateSecretName != "" && tls.Issuer != nil {
		errs = append(errs, field.Forbidden(path.Child("issuer"), "use either a certificate secret or an issuer, not both"))
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
// the running PerconaServerMySQL uses.
func validateTLSChange(c *controller.Context, tls definition.TLSParameters) (field.ErrorList, error) {
	current := &psv1.PerconaServerMySQL{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching current PerconaServerMySQL: %w", err)
	}

	currentIssuer := "/"
	if current.Spec.TLS != nil && current.Spec.TLS.IssuerConf != nil {
		currentIssuer = current.Spec.TLS.IssuerConf.Kind + "/" + current.Spec.TLS.IssuerConf.Name
	}

	path := field.NewPath("spec", "parameters", "tls")
	var errs field.ErrorList
	if current.Spec.SSLSecretName != "" && current.Spec.SSLSecretName != certificateSecretName(c.Name(), tls) {
		errs = append(errs, field.Forbidden(path.Child("certificateSecretName"), "can't change on an existing instance"))
	}
	if currentIssuer != issuerSource(tls) {
		errs = append(errs, field.Forbidden(path.Child("issuer"), "can't change on an existing instance"))
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
		Group: certManagerGroup,
	}
}
