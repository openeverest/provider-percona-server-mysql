// Package definition contains shared types used across the provider definition.
//
// +k8s:openapi-gen=true
package definition

// Parameters defines instance-wide structured parameters that apply to the
// entire cluster (Instance.spec.parameters).
type Parameters struct {
	// TLS configures encryption in transit, between clients and the
	// instance and between its members. The PS operator always encrypts
	// these connections; TLS cannot be disabled.
	// +optional
	TLS *TLSParameters `json:"tls,omitempty"`
}

// TLSMode controls whether client connections require TLS.
// +enum
type TLSMode string

const (
	// TLSModePrefer encrypts traffic between members and accepts clients with
	// or without TLS. The default.
	TLSModePrefer TLSMode = "preferTLS"
	// TLSModeRequire encrypts all traffic and rejects clients without TLS,
	// by setting require_secure_transport=ON on the engine.
	TLSModeRequire TLSMode = "requireTLS"
)

// TLSParameters configures TLS for the instance.
// The certificate comes from, in order of precedence: the certificate
// secret, the cert-manager issuer, or else is generated (through
// cert-manager when it is installed). The source can't change after the
// instance is created, as a new CA would stop existing members and clients
// from trusting the new certificate.
type TLSParameters struct {
	// Mode controls whether client connections require TLS. Changing it on a
	// running instance restarts its pods.
	// +default="preferTLS"
	// +optional
	Mode TLSMode `json:"mode,omitempty"`

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

	// AdditionalNames are DNS names clients use to reach the instance, such
	// as mydb.company.com, added to the in-cluster names of the generated
	// or issued certificate. Not with certificateSecretName: that
	// certificate carries its own names. Changing them on a running
	// instance reissues the certificate and restarts its pods, so it waits
	// for maintenance approval, and needs a certificate issued by
	// cert-manager.
	// +optional
	AdditionalNames []string `json:"additionalNames,omitempty"`
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

	// Group is the API group of the issuer. Only cert-manager.io is
	// supported: the PS operator can't use external issuers yet
	// (https://perconadev.atlassian.net/browse/K8SPS-989).
	// +default="cert-manager.io"
	// +optional
	Group string `json:"group,omitempty"`
}
