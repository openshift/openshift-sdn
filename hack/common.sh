#!/bin/bash

# This script provides common script functions for the hacks
# Requires OSDN_ROOT to be set

# Stolen from https://github.com/openshift/origin/blob/master/hack/common.sh

set -o errexit
set -o nounset
set -o pipefail

# The root of the build/dist directory
OSDN_ROOT=$(
  unset CDPATH
  osdn_root=$(dirname "${BASH_SOURCE}")/..
  cd "${osdn_root}"
  pwd
)

OSDN_OUTPUT_SUBPATH="${OSDN_OUTPUT_SUBPATH:-_output/local}"
OSDN_OUTPUT="${OSDN_ROOT}/${OSDN_OUTPUT_SUBPATH}"
OSDN_OUTPUT_BINPATH="${OSDN_OUTPUT}/bin"
OSDN_LOCAL_BINPATH="${OSDN_ROOT}/_output/local/go/bin"

readonly OSDN_GO_PACKAGE=github.com/openshift/openshift-sdn
readonly OSDN_GOPATH="${OSDN_OUTPUT}/go"

osdn::build::setup_env() {
  if [[ -z "$(which go)" ]]; then
    echo "Can't find 'go' in PATH, please fix and retry."
    exit 2
  fi

  local go_pkg_dir="${OSDN_GOPATH}/src/${OSDN_GO_PACKAGE}"
  local go_pkg_basedir=$(dirname "${go_pkg_dir}")
  mkdir -p "${go_pkg_basedir}"
  mkdir -p "${OSDN_GOPATH}/bin"
  rm -f "${go_pkg_dir}"

  # TODO: This symlink should be relative.
  ln -s "${OSDN_ROOT}" "${go_pkg_dir}"

  GOPATH=${OSDN_GOPATH}:${OSDN_ROOT}/Godeps/_workspace
  export GOPATH
}

# osdn::build::get_version_vars loads the standard version variables as
# ENV vars
osdn::build::get_version_vars() {
  if [[ -n ${OSDN_VERSION_FILE-} ]]; then
    source "${OSDN_VERSION_FILE}"
    return
  fi
  osdn::build::os_version_vars
}

# osdn::build::os_version_vars looks up the current Git vars
osdn::build::os_version_vars() {
  local git=(git --work-tree "${OSDN_ROOT}")

  if [[ -n ${OSDN_GIT_COMMIT-} ]] || OSDN_GIT_COMMIT=$("${git[@]}" rev-parse --short "HEAD^{commit}" 2>/dev/null); then
    if [[ -z ${OSDN_GIT_TREE_STATE-} ]]; then
      # Check if the tree is dirty.  default to dirty
      if git_status=$("${git[@]}" status --porcelain 2>/dev/null) && [[ -z ${git_status} ]]; then
        OSDN_GIT_TREE_STATE="clean"
      else
        OSDN_GIT_TREE_STATE="dirty"
      fi
    fi

    # Use git describe to find the version based on annotated tags.
    # `--always` used since the non-OSE fork might not have any
    # annotated tags
    if [[ -n ${OSDN_GIT_VERSION-} ]] || OSDN_GIT_VERSION=$("${git[@]}" describe --always --abbrev=14 "${OSDN_GIT_COMMIT}^{commit}" 2>/dev/null); then
      if [[ "${OSDN_GIT_TREE_STATE}" == "dirty" ]]; then
        # git describe --dirty only considers changes to existing files, but
        # that is problematic since new untracked .go files affect the build,
        # so use our idea of "dirty" from git status instead.
        OSDN_GIT_VERSION+="-dirty"
      fi

      # Try to match the "git describe" output to a regex to try to extract
      # the "major" and "minor" versions and whether this is the exact tagged
      # version or whether the tree is between two tagged versions.
      if [[ "${OSDN_GIT_VERSION}" =~ ^v([0-9]+)\.([0-9]+)([.-].*)?$ ]]; then
        OSDN_GIT_MAJOR=${BASH_REMATCH[1]}
        OSDN_GIT_MINOR=${BASH_REMATCH[2]}
        if [[ -n "${BASH_REMATCH[3]}" ]]; then
          OSDN_GIT_MINOR+="+"
        fi
      else
        # Set these to "untagged" for trees without any annotated
        # tags, since you can't pass in an empty string for ldflags
        OSDN_GIT_MAJOR="untagged"
        OSDN_GIT_MINOR="untagged"
      fi
    fi
  fi
}

# osdn::build::ldflags calculates the -ldflags argument for building OpenShift
osdn::build::ldflags() {
  (
    # Run this in a subshell to prevent settings/variables from leaking.
    set -o errexit
    set -o nounset
    set -o pipefail

    cd "${OSDN_ROOT}"

    osdn::build::get_version_vars

    declare -a ldflags=()
    ldflags+=(-X "${OSDN_GO_PACKAGE}/pkg/version.majorFromGit" "${OSDN_GIT_MAJOR-}")
    ldflags+=(-X "${OSDN_GO_PACKAGE}/pkg/version.minorFromGit" "${OSDN_GIT_MINOR-}")
    ldflags+=(-X "${OSDN_GO_PACKAGE}/pkg/version.versionFromGit" "${OSDN_GIT_VERSION-}")
    ldflags+=(-X "${OSDN_GO_PACKAGE}/pkg/version.commitFromGit" "${OSDN_GIT_COMMIT-}")

    # The -ldflags parameter takes a single string, so join the output.
    echo "${ldflags[*]-}"
  )
}
