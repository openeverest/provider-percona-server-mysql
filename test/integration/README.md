# Integration tests

Integration tests exercise the provider against a real Kubernetes cluster
using [chainsaw](https://kyverno.github.io/chainsaw/). They apply OpenEverest
`Instance` CRs, assert on the operator-native resources the provider produces,
and verify status propagation back to the `Instance`.

The Percona Server for MySQL operator is scaled to 0. Suites patch operator CR
status directly so they test the provider, not the operator. End-to-end tests
that need a running operator live under `test/e2e-cluster/`.

## Layout

```
test/
  vars.sh                      # Pinned operator/engine versions (sourced by make)
  integration/
    .chainsaw.yaml             # Shared chainsaw configuration (timeouts, reports)
    core/cluster/              # Create, scale, affinity, Service type, delete
    backup/datasource/         # Backup and in-place restore (operator status simulated)
    monitoring/pmm/            # PMM MonitoringConfig mapped onto spec.pmm
  e2e-cluster/
    datasource/backup/         # Real operator backup/restore against SeaweedFS
    monitoring/pmm/            # Real operator: spec.pmm and pmmservertoken
```

PMM monitoring is covered by `monitoring/pmm`. The provider maps the optional
`monitoring` component onto `PerconaServerMySQL.spec.pmm` and copies the
MonitoringConfig API key into the users secret as `pmmservertoken`. Integration
tests pre-create `<name>-secrets` because the operator is scaled to 0. The e2e
suite lets the operator create that secret.

## Running locally

```bash
make test-integration-env-up
make test-integration
make test-integration-core
make test-integration-backup
make test-integration-monitoring-pmm
make test-integration-env-down
```

E2E cluster tests need the operator running (`make deploy-provider-e2e`). Backup
suites also need SeaweedFS (`kubectl apply -f dev/resources/seaweedfs.yaml`):

```bash
make test-e2e-cluster
make test-e2e-cluster-datasource-backup
make test-e2e-cluster-monitoring-pmm
```

## Running in CI

`.github/workflows/ci.yaml` runs the integration suites through
`.github/workflows/integration-test.yaml`. The e2e-cluster workflow is present
but not invoked on default GitHub-hosted runners, matching the PXC provider.
