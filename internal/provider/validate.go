package provider

import (
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// Validate checks the rules about the Instance alone. The runtime runs it once
// the Instance matches the topologies, components, versions and parameter
// schemas the Provider declares.
func (p *Provider) Validate(in controller.ValidationInput) controller.ValidationResult {
	var errs field.ErrorList
	errs = append(errs, validateOrchestrator(in.Instance)...)
	errs = append(errs, validateProxy(in.Instance)...)
	errs = append(errs, validateTLS(in.Instance)...)
	errs = append(errs, validateTLSTransition(in.Instance, in.Old)...)
	return controller.ValidationResult{Errors: errs}
}

// ValidateReferences checks the rules that read other objects: the TLS
// certificate Secret and the running PerconaServerMySQL.
func (p *Provider) ValidateReferences(c *controller.Context) (field.ErrorList, error) {
	return validateTLSReferences(c)
}
