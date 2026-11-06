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
	"errors"
	"fmt"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

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

// tlsParameters returns the instance's TLS parameters.
func tlsParameters(c *controller.Context) (definition.TLSParameters, error) {
	params := definition.Parameters{}
	if raw := c.Instance().Spec.Parameters; raw != nil && len(raw.Raw) > 0 {
		if err := c.DecodeParameters(&params); err != nil {
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
func certificateSecretName(c *controller.Context, tls definition.TLSParameters) string {
	if tls.CertificateSecretName != "" {
		return tls.CertificateSecretName
	}
	return c.Name() + "-ssl"
}

// configureTLS points the PS cluster at the certificate secret and, when set,
// the cert-manager issuer that signs it. The PS operator always encrypts
// these connections: it manages the certificate itself (via cert-manager
// when installed, or a self-signed CA otherwise) unless the secret already
// exists and was not created by the operator, in which case it leaves it
// alone.
func configureTLS(c *controller.Context, cluster *psv1.PerconaServerMySQL, tls definition.TLSParameters) {
	cluster.Spec.SSLSecretName = certificateSecretName(c, tls)
	if issuer := issuerRef(tls); issuer != nil {
		cluster.Spec.TLS = &psv1.TLSSpec{IssuerConf: issuer}
	}
}

// validateTLS validates the TLS parameters, and that the certificate source
// isn't changed on a running instance.
func validateTLS(c *controller.Context) error {
	tls, err := tlsParameters(c)
	if err != nil {
		return err
	}

	if tls.Issuer != nil {
		switch tls.Issuer.Kind {
		case definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer:
		default:
			return fmt.Errorf("tls.issuer.kind must be %s or %s, got %q",
				definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer, tls.Issuer.Kind)
		}
	}

	if tls.CertificateSecretName != "" && tls.Issuer != nil {
		return errors.New("tls: use either a certificate secret or an issuer, not both")
	}

	if tls.CertificateSecretName != "" {
		if err := validateCertificateSecret(c, tls.CertificateSecretName); err != nil {
			return err
		}
	}

	return validateTLSChange(c, tls)
}

func validateCertificateSecret(c *controller.Context, name string) error {
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		return fmt.Errorf("get certificate secret %q: %w", name, err)
	}
	for _, key := range certificateSecretKeys {
		if len(secret.Data[key]) == 0 {
			return fmt.Errorf("certificate secret %q is missing %q", name, key)
		}
	}
	return nil
}

// validateTLSChange rejects a certificate source change on a running
// instance: a new CA would be trusted by neither the existing members nor
// the clients still holding the old one.
func validateTLSChange(c *controller.Context, tls definition.TLSParameters) error {
	current := &psv1.PerconaServerMySQL{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching current PerconaServerMySQL: %w", err)
	}

	currentSecret := current.Spec.SSLSecretName
	currentIssuer := "/"
	if current.Spec.TLS != nil && current.Spec.TLS.IssuerConf != nil {
		currentIssuer = current.Spec.TLS.IssuerConf.Kind + "/" + current.Spec.TLS.IssuerConf.Name
	}

	wantIssuer := "/"
	if tls.Issuer != nil {
		wantIssuer = string(tls.Issuer.Kind) + "/" + tls.Issuer.Name
	}

	if currentSecret != "" && currentSecret != certificateSecretName(c, tls) {
		return errors.New("tls.certificateSecretName can't change on an existing instance")
	}
	if currentIssuer != wantIssuer {
		return errors.New("tls.issuer can't change on an existing instance")
	}

	return nil
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
