#!/usr/bin/env bash
# Checks deploy/terraform with the official image, so neither the developer
# machine nor CI needs terraform installed. The tests use a mock provider and
# need no AWS credentials; init downloads the provider from the registry.
set -euo pipefail
image=hashicorp/terraform:1.17.2@sha256:6e9c1e647c31a960dc9b59e13272f9111d90010c10b717f77ffb324767660475
cd "$(dirname "$0")/.."

tf() {
	docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e CHECKPOINT_DISABLE=1 -e TF_IN_AUTOMATION=1 \
		-v "$PWD":/w -w "/w/$1" "$image" "${@:2}"
}

tf deploy/terraform fmt -check -recursive -diff
for dir in deploy/terraform/bootstrap deploy/terraform; do
	tf "$dir" init -backend=false -input=false -no-color >/dev/null
	tf "$dir" validate -no-color
	tf "$dir" test -no-color
done
