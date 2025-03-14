#!/bin/bash
set -e

kind delete cluster
sh ./scripts/ci/kind-with-olm.sh
sh ./scripts/ci/install-catalog-source.sh
make upgrade-test