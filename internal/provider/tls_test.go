package provider

import (
	"context"
	"encoding/json"
	"testing"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openeverest/provider-percona-server-mysql/definition"
)

const testNamespace = "default"
const testInstanceName = "my-cluster"

// testContext builds a *controller.Context backed by a fake client seeded
// with objs, for an Instance named testInstanceName with the given
// parameters.
func testContext(t *testing.T, params *definition.Parameters, objs ...runtime.Object) *controller.Context {
	t.Helper()

	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := psv1.AddToScheme(s); err != nil {
		t.Fatalf("add psv1 to scheme: %v", err)
	}

	builder := fake.NewClientBuilder().WithScheme(s)
	if len(objs) > 0 {
		builder = builder.WithRuntimeObjects(objs...)
	}
	cl := builder.Build()

	inst := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testInstanceName,
			Namespace: testNamespace,
		},
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal parameters: %v", err)
		}
		inst.Spec.Parameters = &runtime.RawExtension{Raw: raw}
	}

	return controller.NewContext(context.Background(), cl, inst, common_ProviderNameForTest)
}

// common_ProviderNameForTest avoids importing internal/common just for the
// provider name constant in tests that don't otherwise need it.
const common_ProviderNameForTest = "percona-server-mysql"

func certSecret(t *testing.T, name string, complete bool) *corev1.Secret {
	t.Helper()
	data := map[string][]byte{}
	if complete {
		data["tls.crt"] = []byte("cert")
		data["tls.key"] = []byte("key")
		data[caKey] = []byte("ca")
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Data:       data,
	}
}

func TestTLSParameters(t *testing.T) {
	t.Parallel()

	t.Run("no parameters returns zero value", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		tls, err := tlsParameters(c)
		if err != nil {
			t.Fatal(err)
		}
		if tls.CertificateSecretName != "" || tls.Issuer != nil {
			t.Fatalf("got %+v, want zero value", tls)
		}
	})

	t.Run("issuer kind defaults to Issuer", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{
				Issuer: &definition.IssuerReference{Name: "my-issuer"},
			},
		})
		tls, err := tlsParameters(c)
		if err != nil {
			t.Fatal(err)
		}
		if tls.Issuer == nil || tls.Issuer.Kind != definition.IssuerKindIssuer {
			t.Fatalf("got %+v, want kind defaulted to Issuer", tls.Issuer)
		}
	})

	t.Run("issuer with empty name is dropped", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{
				Issuer: &definition.IssuerReference{Kind: definition.IssuerKindClusterIssuer},
			},
		})
		tls, err := tlsParameters(c)
		if err != nil {
			t.Fatal(err)
		}
		if tls.Issuer != nil {
			t.Fatalf("got %+v, want nil issuer", tls.Issuer)
		}
	})
}

func TestCertificateSecretName(t *testing.T) {
	t.Parallel()

	c := testContext(t, nil)

	if got, want := certificateSecretName(c, definition.TLSParameters{}), testInstanceName+"-ssl"; got != want {
		t.Fatalf("default secret name = %q, want %q", got, want)
	}
	if got, want := certificateSecretName(c, definition.TLSParameters{CertificateSecretName: "custom"}), "custom"; got != want {
		t.Fatalf("custom secret name = %q, want %q", got, want)
	}
}

func TestConfigureTLS(t *testing.T) {
	t.Parallel()

	t.Run("default", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		cluster := &psv1.PerconaServerMySQL{}
		configureTLS(c, cluster, definition.TLSParameters{})
		if cluster.Spec.SSLSecretName != testInstanceName+"-ssl" {
			t.Fatalf("SSLSecretName = %q", cluster.Spec.SSLSecretName)
		}
		if cluster.Spec.TLS != nil {
			t.Fatalf("TLS = %+v, want nil (operator manages the certificate)", cluster.Spec.TLS)
		}
	})

	t.Run("custom secret", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		cluster := &psv1.PerconaServerMySQL{}
		configureTLS(c, cluster, definition.TLSParameters{CertificateSecretName: "my-certs"})
		if cluster.Spec.SSLSecretName != "my-certs" {
			t.Fatalf("SSLSecretName = %q", cluster.Spec.SSLSecretName)
		}
	})

	t.Run("issuer", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		cluster := &psv1.PerconaServerMySQL{}
		configureTLS(c, cluster, definition.TLSParameters{
			Issuer: &definition.IssuerReference{Name: "my-issuer", Kind: definition.IssuerKindClusterIssuer},
		})
		if cluster.Spec.TLS == nil || cluster.Spec.TLS.IssuerConf == nil {
			t.Fatal("TLS.IssuerConf not set")
		}
		want := cmmeta.ObjectReference{Name: "my-issuer", Kind: "ClusterIssuer", Group: certManagerGroup}
		if *cluster.Spec.TLS.IssuerConf != want {
			t.Fatalf("IssuerConf = %+v, want %+v", *cluster.Spec.TLS.IssuerConf, want)
		}
	})
}

func TestValidateTLS(t *testing.T) {
	t.Parallel()

	t.Run("no parameters is ok", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		if err := validateTLS(c); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("invalid issuer kind rejected", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "i", Kind: "Bogus"}},
		})
		if err := validateTLS(c); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("secret and issuer together rejected", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{
				CertificateSecretName: "my-certs",
				Issuer:                &definition.IssuerReference{Name: "i"},
			},
		}, certSecret(t, "my-certs", true))
		if err := validateTLS(c); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("certificate secret missing is rejected", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{CertificateSecretName: "does-not-exist"},
		})
		if err := validateTLS(c); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("certificate secret missing keys is rejected", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{CertificateSecretName: "incomplete"},
		}, certSecret(t, "incomplete", false))
		if err := validateTLS(c); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("valid certificate secret is ok", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{CertificateSecretName: "my-certs"},
		}, certSecret(t, "my-certs", true))
		if err := validateTLS(c); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("changing certificate secret on existing instance rejected", func(t *testing.T) {
		t.Parallel()
		existing := &psv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: testNamespace},
			Spec:       psv1.PerconaServerMySQLSpec{SSLSecretName: "old-certs"},
		}
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{CertificateSecretName: "new-certs"},
		}, existing, certSecret(t, "new-certs", true))
		if err := validateTLS(c); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("changing issuer on existing instance rejected", func(t *testing.T) {
		t.Parallel()
		existing := &psv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: testNamespace},
			Spec: psv1.PerconaServerMySQLSpec{
				SSLSecretName: testInstanceName + "-ssl",
				TLS:           &psv1.TLSSpec{IssuerConf: &cmmeta.ObjectReference{Name: "old-issuer", Kind: "Issuer"}},
			},
		}
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "new-issuer"}},
		}, existing)
		if err := validateTLS(c); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("unchanged configuration on existing instance is ok", func(t *testing.T) {
		t.Parallel()
		existing := &psv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: testNamespace},
			Spec: psv1.PerconaServerMySQLSpec{
				SSLSecretName: testInstanceName + "-ssl",
			},
		}
		c := testContext(t, nil, existing)
		if err := validateTLS(c); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTLSConnectionCA(t *testing.T) {
	t.Parallel()

	t.Run("no secret name returns nil", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		ca, err := tlsConnectionCA(c, &psv1.PerconaServerMySQL{})
		if err != nil {
			t.Fatal(err)
		}
		if ca != nil {
			t.Fatalf("got %q, want nil", ca)
		}
	})

	t.Run("secret not found yet returns nil", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		cluster := &psv1.PerconaServerMySQL{Spec: psv1.PerconaServerMySQLSpec{SSLSecretName: "not-there"}}
		ca, err := tlsConnectionCA(c, cluster)
		if err != nil {
			t.Fatal(err)
		}
		if ca != nil {
			t.Fatalf("got %q, want nil", ca)
		}
	})

	t.Run("returns the CA from the secret", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil, certSecret(t, "my-ssl", true))
		cluster := &psv1.PerconaServerMySQL{Spec: psv1.PerconaServerMySQLSpec{SSLSecretName: "my-ssl"}}
		ca, err := tlsConnectionCA(c, cluster)
		if err != nil {
			t.Fatal(err)
		}
		if string(ca) != "ca" {
			t.Fatalf("got %q, want %q", ca, "ca")
		}
	})
}
