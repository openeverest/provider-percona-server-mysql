package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	s3class "github.com/openeverest/openeverest/v2/backupstorageclasses/s3"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mysql/definition/backupclasses"
)

func TestShippedBackupClasses(t *testing.T) {
	t.Parallel()

	p := New()
	classes := p.ShippedBackupClasses()
	require.Len(t, classes, 1)
	class := classes[0]
	assert.Equal(t, backupClassName, class.Name)
	assert.Equal(t, p.Name(), class.Spec.ExecutorProvider())

	entry := class.Spec.FindBackupStorageClass(s3class.ClassName)
	require.NotNil(t, entry)
	assert.Equal(t, []string{s3class.AuthModeAccessKey}, entry.AuthModes)
	require.NotNil(t, entry.ParametersSchema)
	require.NotNil(t, entry.ParametersSchema.OpenAPIV3Schema)
	require.NotNil(t, entry.ParametersSchema.OpenAPIV3Schema.AdditionalProperties)
	assert.False(t, entry.ParametersSchema.OpenAPIV3Schema.AdditionalProperties.Allows,
		"the entry carries the closed s3 contract of core",
	)
	assert.Equal(t, &backupv1alpha1.BackupStorageTLSSupport{
		CustomCA:           backupv1alpha1.TLSOptionNone,
		InsecureSkipVerify: backupv1alpha1.TLSOptionSupported,
	}, entry.TLS)
	assert.Equal(t, controller.TLSSupport{SkipVerify: true}, backupclasses.TLSSupport(backupClassName, s3class.ClassName))
}
