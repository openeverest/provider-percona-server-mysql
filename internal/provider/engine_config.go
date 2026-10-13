package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"sigs.k8s.io/yaml"

	"github.com/openeverest/provider-percona-server-mysql/definition/components"
	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

func engineConfigurationFromComponent(engine corev1alpha1.ComponentSpec) (map[string]any, error) {
	if engine.Parameters == nil || len(engine.Parameters.Raw) == 0 {
		return nil, nil
	}

	params := &components.MysqlParameters{}
	if err := json.Unmarshal(engine.Parameters.Raw, params); err != nil {
		return nil, fmt.Errorf("decode %q component parameters: %w", common.ComponentEngine, err)
	}
	if strings.TrimSpace(params.Configuration) == "" {
		return nil, nil
	}

	var mysqlParams map[string]any
	if err := yaml.Unmarshal([]byte(params.Configuration), &mysqlParams); err != nil {
		return nil, fmt.Errorf("parse %q component configuration: %w", common.ComponentEngine, err)
	}
	if len(mysqlParams) == 0 {
		return nil, nil
	}

	return mysqlParams, nil
}

func applyEngineConfiguration(cluster *psv1.PerconaServerMySQL, engine corev1alpha1.ComponentSpec) error {
	params, err := engineConfigurationFromComponent(engine)
	if err != nil {
		return err
	}
	if params == nil {
		cluster.Spec.MySQL.Configuration = ""
		return nil
	}

	rendered, err := renderMySQLConfiguration(params)
	if err != nil {
		return err
	}
	cluster.Spec.MySQL.Configuration = rendered
	return nil
}

// renderMySQLConfiguration converts a parameter map into the key=value snippet
// the Percona Server for MySQL operator writes to my.cnf. encoding/json sorts
// object keys, so the snippet is stable across reconciles.
func renderMySQLConfiguration(params map[string]any) (string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode %q component configuration: %w", common.ComponentEngine, err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("decode %q component configuration: %w", common.ComponentEngine, err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return "", fmt.Errorf("%q component configuration must be an object", common.ComponentEngine)
	}

	var lines []string
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("decode %q component configuration key: %w", common.ComponentEngine, err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", fmt.Errorf("%q component configuration key %v is not a string", common.ComponentEngine, keyToken)
		}

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return "", fmt.Errorf("decode %q component configuration value for %q: %w", common.ComponentEngine, key, err)
		}
		lines = append(lines, key+"="+strings.TrimSpace(string(value)))
	}
	return strings.Join(lines, "\n"), nil
}
