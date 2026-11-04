// Package definition contains shared types used across the provider definition.
//
// +k8s:openapi-gen=true
package definition

// Parameters defines instance-wide structured parameters that apply to the
// entire cluster (Instance.spec.parameters).
type Parameters struct {
	// TLS configures the certificates used to encrypt connections to the
	// instance and between its members. The PS operator always encrypts
	// these connections; TLS cannot be disabled.
	// +optional
	TLS *TLSParameters `json:"tls,omitempty"`
}

// TLSParameters configures the certificates for the instance's TLS traffic.
// The certificate comes from, in order of precedence: the certificate
// secret, the cert-manager issuer, or else is generated (through
// cert-manager when it is installed). The source can't change after the
// instance is created, as a new CA would stop existing members and clients
// from trusting the new certificate.
type TLSParameters struct {
	// CertificateSecretName names a Secret in the instance namespace with
	// the certificate the instance presents to clients and other members:
	// tls.crt, tls.key and ca.crt. Its subject alternative names must cover
	// the instance's service and pod names.
	// +optional
	CertificateSecretName string `json:"certificateSecretName,omitempty"`

	// Issuer is the cert-manager issuer that signs the instance's
	// certificate, instead of a self-signed CA created for the instance.
	// +optional
	Issuer *IssuerReference `json:"issuer,omitempty"`
}

// IssuerKind is the kind of a cert-manager issuer.
// +enum
type IssuerKind string

const (
	// IssuerKindIssuer is an Issuer in the instance namespace.
	IssuerKindIssuer IssuerKind = "Issuer"
	// IssuerKindClusterIssuer is a cluster-wide ClusterIssuer.
	IssuerKindClusterIssuer IssuerKind = "ClusterIssuer"
)

// IssuerReference refers to a cert-manager issuer.
type IssuerReference struct {
	// Name of the issuer.
	// +optional
	Name string `json:"name,omitempty"`

	// Kind of the issuer.
	// +default="Issuer"
	// +optional
	Kind IssuerKind `json:"kind,omitempty"`
}
