#!/bin/bash

set -e

OSDN_ROOT=$(
  unset CDPATH
  osdn_root=$(dirname "${BASH_SOURCE}")/..
  cd "${osdn_root}"
  pwd
)

source "${OSDN_ROOT}/hack/common.sh"

osdn::build::setup_env
version_ldflags=$(osdn::build::ldflags)
go install \
   -ldflags "${version_ldflags}" \
   ${OSDN_GO_PACKAGE}
cp -f ovs-simple/bin/openshift-sdn-simple-setup-node.sh ${OSDN_GOPATH}/bin
