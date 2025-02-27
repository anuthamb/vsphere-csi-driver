#!/bin/bash

# Copyright 2019 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

lintout=$(mktemp)
lintout_filtered=$(mktemp)

function cleanup() {
	rm -f "$lintout" "$lintout_filtered"
}

trap cleanup EXIT

# Change directories to the parent directory of the one in which this
# script is located.
cd "$(dirname "${BASH_SOURCE[0]}")/.."

go get golang.org/x/lint/golint
# shellcheck disable=SC2046
# shellcheck disable=SC1083
$(go env GOPATH)/bin/golint $(go list ./... | grep -v /vendor/) > "$lintout"

# Remove autogenerated files from linter output
lintout_filtered=$(sed '/generate/d; /pb/d; /proto/d; /CnsNodeVmAttachment/d ' < "$lintout")

# Exit with error code if lint checks failed

if [ -z "$lintout_filtered" ]; then
	exit 0
else
	exit 1
fi

