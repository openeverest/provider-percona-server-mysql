#!/bin/bash

## ===== General environment variables for the Percona Operator tests =====
export OPERATOR_ROOT_PATH=${OPERATOR_ROOT_PATH:-${PWD}}
echo "OPERATOR_ROOT_PATH=${OPERATOR_ROOT_PATH}"

## ======= Upstream DB operators params for testing ===============

# Recommended Percona Server for MySQL operator version for tests.
# Keep in sync with PS_OPERATOR_VERSION in the Makefile and go.mod.
export PS_OPERATOR_VERSION=${PS_OPERATOR_VERSION:-"1.2.0"}
echo "PS_OPERATOR_VERSION=${PS_OPERATOR_VERSION}"

# Recommended engine version for tests (default version bundle).
export PS_DB_ENGINE_VERSION=${PS_DB_ENGINE_VERSION:-"8.4.10-10.1"}
echo "PS_DB_ENGINE_VERSION=${PS_DB_ENGINE_VERSION}"

# Previous versions for upgrade tests.
export PREVIOUS_PS_DB_ENGINE_VERSION=${PREVIOUS_PS_DB_ENGINE_VERSION:-"8.0.46-37.1"}
echo "PREVIOUS_PS_DB_ENGINE_VERSION=${PREVIOUS_PS_DB_ENGINE_VERSION}"

export PREVIOUS_PS_OPERATOR_VERSION=${PREVIOUS_PS_OPERATOR_VERSION:-"1.1.0"}
echo "PREVIOUS_PS_OPERATOR_VERSION=${PREVIOUS_PS_OPERATOR_VERSION}"
