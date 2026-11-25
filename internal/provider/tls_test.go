package provider

import (
	"context"
	"encoding/json"
	"testing"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
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
		if tls.CertificateSecretName != "" || tls.Issuer != nil || tls.AdditionalNames != nil {
			t.Fatalf("got %+v, want zero value", tls)
		}
		if tls.Mode != definition.TLSModePrefer {
			t.Fatalf("mode = %q, want %q", tls.Mode, definition.TLSModePrefer)
		}
	})

	t.Run("issuer group defaults to cert-manager.io", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, &definition.Parameters{
			TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "my-issuer"}},
		})
		tls, err := tlsParameters(c)
		require.NoError(t, err)
		assert.Equal(t, certManagerGroup, tls.Issuer.Group)
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

	if got, want := certificateSecretName(testInstanceName, definition.TLSParameters{}), testInstanceName+"-ssl"; got != want {
		t.Fatalf("default secret name = %q, want %q", got, want)
	}
	if got, want := certificateSecretName(testInstanceName, definition.TLSParameters{CertificateSecretName: "custom"}), "custom"; got != want {
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
		want := cmmeta.IssuerReference{Name: "my-issuer", Kind: "ClusterIssuer", Group: certManagerGroup}
		if *cluster.Spec.TLS.IssuerConf != want {
			t.Fatalf("IssuerConf = %+v, want %+v", *cluster.Spec.TLS.IssuerConf, want)
		}
		assert.Nil(t, cluster.Spec.TLS.SANs)
	})

	t.Run("additional names", func(t *testing.T) {
		t.Parallel()
		c := testContext(t, nil)
		cluster := &psv1.PerconaServerMySQL{}
		configureTLS(c, cluster, definition.TLSParameters{AdditionalNames: []string{"db.example.com"}})
		require.NotNil(t, cluster.Spec.TLS)
		assert.Nil(t, cluster.Spec.TLS.IssuerConf)
		assert.Equal(t, []string{"db.example.com"}, cluster.Spec.TLS.SANs)
	})
}

// errorFields returns the field path of each error.
func errorFields(errs field.ErrorList) []string {
	var fields []string
	for _, err := range errs {
		fields = append(fields, err.Field)
	}
	return fields
}

// testInstance is the Instance testContext would build, in the given phase.
func testInstance(t *testing.T, params *definition.Parameters, phase corev1alpha1.InstancePhase) *corev1alpha1.Instance {
	t.Helper()
	in := testContext(t, params).Instance()
	in.Status.Phase = phase
	return in
}

func TestValidateTLS(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		params *definition.Parameters
		fields []string
	}{
		{name: "no parameters is ok"},
		{
			name: "certificate secret is ok",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{CertificateSecretName: "my-certs"},
			},
		},
		{
			name: "invalid issuer kind rejected",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "i", Kind: "Bogus"}},
			},
			fields: []string{"spec.parameters.tls.issuer.kind"},
		},
		{
			name:   "requireTLS is ok",
			params: &definition.Parameters{TLS: &definition.TLSParameters{Mode: definition.TLSModeRequire}},
		},
		{
			name:   "disabled mode rejected",
			params: &definition.Parameters{TLS: &definition.TLSParameters{Mode: "disabled"}},
			fields: []string{"spec.parameters.tls.mode"},
		},
		{
			name: "cert-manager group is ok",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "i", Group: "cert-manager.io"}},
			},
		},
		{
			name: "external issuer group rejected",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "i", Group: "awspca.cert-manager.io"}},
			},
			fields: []string{"spec.parameters.tls.issuer.group"},
		},
		{
			name: "dns and wildcard names are ok",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{AdditionalNames: []string{"db.example.com", "*.example.com"}},
			},
		},
		{
			name: "ip, duplicate and invalid names rejected",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{AdditionalNames: []string{"10.0.0.1", "db.example.com", "db.example.com", "Not_DNS"}},
			},
			fields: []string{
				"spec.parameters.tls.additionalNames[0]",
				"spec.parameters.tls.additionalNames[2]",
				"spec.parameters.tls.additionalNames[3]",
			},
		},
		{
			name: "names with certificate secret rejected",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{CertificateSecretName: "my-certs", AdditionalNames: []string{"db.example.com"}},
			},
			fields: []string{"spec.parameters.tls.additionalNames"},
		},
		{
			name: "secret and issuer together rejected",
			params: &definition.Parameters{
				TLS: &definition.TLSParameters{
					CertificateSecretName: "my-certs",
					Issuer:                &definition.IssuerReference{Name: "i"},
				},
			},
			fields: []string{"spec.parameters.tls.issuer"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.fields, errorFields(validateTLS(testInstance(t, tc.params, ""))))
		})
	}
}

func TestValidateTLSTransition(t *testing.T) {
	t.Parallel()

	secret := func(name string) *definition.Parameters {
		return &definition.Parameters{TLS: &definition.TLSParameters{CertificateSecretName: name}}
	}
	issuer := func(name string) *definition.Parameters {
		return &definition.Parameters{TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: name}}}
	}

	for _, tc := range []struct {
		name     string
		old, new *definition.Parameters
		oldPhase corev1alpha1.InstancePhase
		noOld    bool
		fields   []string
	}{
		{name: "no old instance", new: secret("new-certs"), noOld: true},
		{name: "pending instance can change", old: secret("old-certs"), new: secret("new-certs"), oldPhase: corev1alpha1.InstancePhasePending},
		{
			name:     "changing certificate secret rejected",
			old:      secret("old-certs"),
			new:      secret("new-certs"),
			oldPhase: corev1alpha1.InstancePhaseProvisioned,
			fields:   []string{"spec.parameters.tls.certificateSecretName"},
		},
		{
			name:     "changing issuer rejected",
			old:      issuer("old-issuer"),
			new:      issuer("new-issuer"),
			oldPhase: corev1alpha1.InstancePhaseProvisioned,
			fields:   []string{"spec.parameters.tls.issuer"},
		},
		{
			name:     "naming the default secret is no change",
			new:      secret(testInstanceName + "-ssl"),
			oldPhase: corev1alpha1.InstancePhaseProvisioned,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var old *corev1alpha1.Instance
			if !tc.noOld {
				old = testInstance(t, tc.old, tc.oldPhase)
			}
			assert.Equal(t, tc.fields, errorFields(validateTLSTransition(testInstance(t, tc.new, ""), old)))
		})
	}
}

func TestValidateTLSReferences(t *testing.T) {
	t.Parallel()

	existing := func(spec psv1.PerconaServerMySQLSpec) *psv1.PerconaServerMySQL {
		return &psv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: testNamespace},
			Spec:       spec,
		}
	}

	for _, tc := range []struct {
		name   string
		params *definition.Parameters
		objs   []runtime.Object
		fields []string
	}{
		{name: "no parameters is ok"},
		{
			name:   "certificate secret missing is rejected",
			params: &definition.Parameters{TLS: &definition.TLSParameters{CertificateSecretName: "does-not-exist"}},
			fields: []string{"spec.parameters.tls.certificateSecretName"},
		},
		{
			name:   "certificate secret missing keys is rejected",
			params: &definition.Parameters{TLS: &definition.TLSParameters{CertificateSecretName: "incomplete"}},
			objs:   []runtime.Object{certSecret(t, "incomplete", false)},
			fields: []string{"spec.parameters.tls.certificateSecretName"},
		},
		{
			name:   "valid certificate secret is ok",
			params: &definition.Parameters{TLS: &definition.TLSParameters{CertificateSecretName: "my-certs"}},
			objs:   []runtime.Object{certSecret(t, "my-certs", true)},
		},
		{
			name:   "changing certificate secret on existing instance rejected",
			params: &definition.Parameters{TLS: &definition.TLSParameters{CertificateSecretName: "new-certs"}},
			objs: []runtime.Object{
				existing(psv1.PerconaServerMySQLSpec{SSLSecretName: "old-certs"}),
				certSecret(t, "new-certs", true),
			},
			fields: []string{"spec.parameters.tls.certificateSecretName"},
		},
		{
			name:   "changing issuer on existing instance rejected",
			params: &definition.Parameters{TLS: &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "new-issuer"}}},
			objs: []runtime.Object{existing(psv1.PerconaServerMySQLSpec{
				SSLSecretName: testInstanceName + "-ssl",
				TLS:           &psv1.TLSSpec{IssuerConf: &cmmeta.IssuerReference{Name: "old-issuer", Kind: "Issuer"}},
			})},
			fields: []string{"spec.parameters.tls.issuer"},
		},
		{
			name: "unchanged configuration on existing instance is ok",
			objs: []runtime.Object{existing(psv1.PerconaServerMySQLSpec{SSLSecretName: testInstanceName + "-ssl"})},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, err := validateTLSReferences(testContext(t, tc.params, tc.objs...))
			require.NoError(t, err)
			assert.Equal(t, tc.fields, errorFields(errs))
		})
	}
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

func TestApplyTLSMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		mode          definition.TLSMode
		configuration string
		want          string
	}{
		{name: "preferTLS leaves the configuration", mode: definition.TLSModePrefer, configuration: "max_connections=500", want: "max_connections=500"},
		{name: "requireTLS on empty configuration", mode: definition.TLSModeRequire, want: "require_secure_transport=ON"},
		{name: "requireTLS appends", mode: definition.TLSModeRequire, configuration: "max_connections=500", want: "max_connections=500\nrequire_secure_transport=ON"},
		{name: "requireTLS keeps the user value", mode: definition.TLSModeRequire, configuration: `require-secure-transport="OFF"`, want: `require-secure-transport="OFF"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cluster := &psv1.PerconaServerMySQL{}
			cluster.Spec.MySQL.Configuration = tc.configuration
			applyTLSMode(cluster, definition.TLSParameters{Mode: tc.mode})
			assert.Equal(t, tc.want, cluster.Spec.MySQL.Configuration)
		})
	}
}

func TestRequiresSecureTransport(t *testing.T) {
	t.Parallel()

	for configuration, want := range map[string]bool{
		"":                                     false,
		"max_connections=500":                  false,
		"require_secure_transport=ON":          true,
		"a=1\nrequire_secure_transport=\"on\"": true,
		"require-secure-transport=1":           true,
		"require_secure_transport=OFF":         false,
	} {
		cluster := &psv1.PerconaServerMySQL{}
		cluster.Spec.MySQL.Configuration = configuration
		assert.Equal(t, want, requiresSecureTransport(cluster), configuration)
	}
}

func TestMySQLExporterConnectsWithTLSWhenRequired(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, mysqldExporterContainer("img", "users", false).Args, "--tls.insecure-skip-verify")
	assert.Contains(t, mysqldExporterContainer("img", "users", true).Args, "--tls.insecure-skip-verify")
}

func TestPMMMySQLParamsConnectWithTLSWhenRequired(t *testing.T) {
	t.Parallel()

	c := testContext(t, nil)
	cluster := &psv1.PerconaServerMySQL{}
	assert.NotContains(t, pmmMySQLParams(c, cluster), "--tls")

	cluster.Spec.MySQL.Configuration = "require_secure_transport=ON"
	assert.Contains(t, pmmMySQLParams(c, cluster), " --tls --tls-skip-verify")
}

// existingCluster is a running PS cluster whose certificate has the given
// additional names.
func existingCluster(names ...string) *psv1.PerconaServerMySQL {
	cluster := &psv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: testNamespace},
		Spec:       psv1.PerconaServerMySQLSpec{SSLSecretName: testInstanceName + "-ssl"},
	}
	if len(names) > 0 {
		cluster.Spec.TLS = &psv1.TLSSpec{SANs: names}
	}
	return cluster
}

// operatorCertificate is the cert-manager Certificate the operator created.
func operatorCertificate(dnsNames ...string) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(testInstanceName + "-ssl")
	cert.SetNamespace(testNamespace)
	names := make([]any, 0, len(dnsNames))
	for _, n := range dnsNames {
		names = append(names, n)
	}
	cert.Object["spec"] = map[string]any{"dnsNames": names}
	return cert
}

func certificateNames(t *testing.T, c *controller.Context) []string {
	t.Helper()
	cert, err := getCertificate(c, testInstanceName+"-ssl")
	require.NoError(t, err)
	require.NotNil(t, cert)
	names, _, err := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	require.NoError(t, err)
	return names
}

func TestGateTLSNamesNewInstance(t *testing.T) {
	t.Parallel()

	tls := definition.TLSParameters{AdditionalNames: []string{"db.example.com"}}
	c := testContext(t, &definition.Parameters{TLS: &tls})
	cluster := &psv1.PerconaServerMySQL{}
	configureTLS(c, cluster, tls)

	require.NoError(t, gateTLSNames(c, cluster))
	assert.Equal(t, []string{"db.example.com"}, cluster.Spec.TLS.SANs)
	assert.Empty(t, c.GetPendingMaintenance())
}

func TestGateTLSNamesHoldsUnapprovedChange(t *testing.T) {
	t.Parallel()

	tls := definition.TLSParameters{AdditionalNames: []string{"new.example.com"}}
	c := testContext(t, &definition.Parameters{TLS: &tls},
		existingCluster("old.example.com"), operatorCertificate("*.db-mysql", "old.example.com"))
	cluster := &psv1.PerconaServerMySQL{}
	configureTLS(c, cluster, tls)

	require.NoError(t, gateTLSNames(c, cluster))
	assert.Equal(t, []string{"old.example.com"}, cluster.Spec.TLS.SANs)
	require.Len(t, c.GetPendingMaintenance(), 1)
	assert.Equal(t, tlsNamesToken(tls.AdditionalNames), c.GetPendingMaintenance()[0].ApprovalToken)
	assert.Equal(t, controller.MaintenanceRollingRestart, c.GetPendingMaintenance()[0].Severity)
	assert.Equal(t, []string{"*.db-mysql", "old.example.com"}, certificateNames(t, c))
}

func TestGateTLSNamesHoldsUnapprovedRemoval(t *testing.T) {
	t.Parallel()

	c := testContext(t, nil, existingCluster("old.example.com"), operatorCertificate("*.db-mysql", "old.example.com"))
	cluster := &psv1.PerconaServerMySQL{}
	configureTLS(c, cluster, definition.TLSParameters{})

	require.NoError(t, gateTLSNames(c, cluster))
	require.NotNil(t, cluster.Spec.TLS)
	assert.Equal(t, []string{"old.example.com"}, cluster.Spec.TLS.SANs)
	require.Len(t, c.GetPendingMaintenance(), 1)
}

func TestGateTLSNamesReissuesApprovedChange(t *testing.T) {
	t.Parallel()

	wanted := []string{"new.example.com"}
	tls := definition.TLSParameters{AdditionalNames: wanted}
	c := testContext(t, &definition.Parameters{TLS: &tls},
		existingCluster("old.example.com"), operatorCertificate("*.db-mysql", "old.example.com", "db-mysql-primary"))
	c.Instance().Spec.Maintenance = &corev1alpha1.MaintenanceSpec{Approved: tlsNamesToken(wanted)}
	cluster := &psv1.PerconaServerMySQL{}
	configureTLS(c, cluster, tls)

	require.NoError(t, gateTLSNames(c, cluster))
	assert.Equal(t, wanted, cluster.Spec.TLS.SANs)
	assert.Empty(t, c.GetPendingMaintenance())
	assert.Equal(t, []string{"*.db-mysql", "db-mysql-primary", "new.example.com"}, certificateNames(t, c))
}

func TestValidateTLSReferencesAdditionalNamesChange(t *testing.T) {
	t.Parallel()

	params := &definition.Parameters{TLS: &definition.TLSParameters{AdditionalNames: []string{"db.example.com"}}}

	// A certificate issued by cert-manager can be reissued with the names.
	errs, err := validateTLSReferences(testContext(t, params, existingCluster(), operatorCertificate("*.db-mysql")))
	require.NoError(t, err)
	assert.Empty(t, errs)

	// One from the CA the operator generated can't.
	errs, err = validateTLSReferences(testContext(t, params, existingCluster()))
	require.NoError(t, err)
	assert.Equal(t, []string{"spec.parameters.tls.additionalNames"}, errorFields(errs))

	// Unchanged names are fine either way.
	errs, err = validateTLSReferences(testContext(t, params, existingCluster("db.example.com")))
	require.NoError(t, err)
	assert.Empty(t, errs)
}
