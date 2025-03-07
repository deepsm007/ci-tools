# Old-skool build tools.
#
# Targets (see each target for more information):
#   all: Build code.
#   build: Build code.
#   test: Run all tests.
#   clean: Clean up.
#

OUT_DIR = _output
OS_OUTPUT_GOPATH ?= 1
SHELL=/usr/bin/env bash -eo pipefail

export GOFLAGS
export TESTFLAGS

# Tests run using `make` are most often run by the CI system, so we are OK to
# assume the user wants jUnit output and will turn it off if they don't.
JUNIT_REPORT ?= true

all build: install
.PHONY: all build

# Verify code conventions are properly setup.
#
# Example:
#   make verify
verify:
	{ \
	hack/verify-gofmt.sh ||r=1;\
	hack/verify-govet.sh ||r=1;\
	make verify-gen || rc=1;\
	exit $$r ;\
	}
.PHONY: verify

# Verify code conventions are properly setup.
#
# Example:
#   make lint
lint:
	./hack/lint.sh
.PHONY: lint

# Run unit tests.
#
# Args:
#   GOFLAGS: Extra flags to pass to 'go' when building.
#
# Example:
#   make test
test: cmd/vault-secret-collection-manager/index.js
	TESTFLAGS="$(TESTFLAGS)" hack/test-go.sh
.PHONY: test

# Remove all build artifacts.
#
# Example:
#   make clean
clean:
	rm -rf $(OUT_DIR)
.PHONY: clean

# Format all source code.
#
# Example:
#   make format
format: frontend-format gofmt
.PHONY: format

# Format all Go source code.
#
# Example:
#   make gofmt
gofmt: cmd/vault-secret-collection-manager/index.js
	gofmt -s -w $(shell go list --tags e2e,e2e_framework -f '{{ .Dir }}' ./... )
.PHONY: gofmt

# Update vendored code and manifests to ensure formatting.
#
# Example:
#   make update-vendor
update-vendor:
	docker run --rm \
		--user=$$UID \
		-v $$(go env GOCACHE):/.cache:Z \
		-v $$PWD:/go/src/github.com/openshift/ci-tools:Z \
		-w /go/src/github.com/openshift/ci-tools \
		-e GO111MODULE=on \
		-e GOPROXY=https://proxy.golang.org \
		-e GOCACHE=/tmp/go-build-cache \
		registry.ci.openshift.org/openshift/release:rhel-9-release-golang-1.23-openshift-4.19 \
		/bin/bash -c "go mod tidy && go mod vendor"
.PHONY: update-vendor

# Validate vendored code and manifests to ensure formatting.
#
# Example:
#   make validate-vendor
validate-vendor:
	go version
	GO111MODULE=on GOPROXY=https://proxy.golang.org go mod tidy
	GO111MODULE=on GOPROXY=https://proxy.golang.org go mod vendor
	git status -s ./vendor/ go.mod go.sum
	test -z "$$(git status -s ./vendor/ go.mod go.sum | grep -v vendor/modules.txt)"
.PHONY: validate-vendor

# Use verbosity by default, allow users to opt out
VERBOSE := $(if $(QUIET),,-v )

# Install Go binaries to $GOPATH/bin.
#
# Example:
#   make install
install: cmd/vault-secret-collection-manager/index.js
	go install $(VERBOSE)./cmd/...
.PHONY: install

cmd/vault-secret-collection-manager/index.js: cmd/vault-secret-collection-manager/index.ts
	hack/compile-typescript.sh

# Install Go binaries to $GOPATH/bin.
# Set version and name variables.
#
# Example:
#   make production-install
production-install: cmd/vault-secret-collection-manager/index.js cmd/pod-scaler/frontend/dist cmd/repo-init/frontend/dist
	hack/install.sh no-race remove-dummy
.PHONY: production-install

# Install Go binaries with enabled race detector to $GOPATH/bin.
# Set version and name variables.
#
# Example:
#   make production-install
race-install: cmd/vault-secret-collection-manager/index.js cmd/pod-scaler/frontend/dist cmd/repo-init/frontend/dist
	hack/install.sh race keep-dummy

# Run integration tests.
#
# Accepts a specific suite to run as an argument.
#
# Example:
#   make integration
#   make integration SUITE=multi-stage
integration:
	@set -e; \
		if [[ -n $$OPENSHIFT_CI ]]; then count=10; else count=1; fi && \
		for try in $$(seq $$count); do \
			echo "Try $$try" && \
			hack/test-integration.sh $(SUITE) ; \
		done
.PHONY: integration

TMPDIR ?= /tmp
TAGS ?= e2e,e2e_framework
PACKAGES ?= ./test/e2e/...

# Run e2e tests.
#
# Accepts specific suites to run via `$PACKAGES`.
#
# Example:
#   make e2e
#   make e2e PACKAGES=test/e2e/pod-scaler
#   make e2e PACKAGES=test/e2e/pod-scaler TESTFLAGS='--run TestProduce'
#   make e2e PACKAGES=test/e2e/pod-scaler TESTFLAGS='--count 1'
e2e: $(TMPDIR)/.boskos-credentials
	BOSKOS_CREDENTIALS_FILE="$(TMPDIR)/.boskos-credentials" PACKAGES="$(PACKAGES)" TESTFLAGS="$(TESTFLAGS) -tags $(TAGS) -timeout 70m -parallel 100" hack/test-go.sh
.PHONY: e2e

$(TMPDIR)/.boskos-credentials:
	echo -n "u:p" > $(TMPDIR)/.boskos-credentials

CLUSTER ?= build01

# Dependencies required to execute the E2E tests outside of the CI environment.
local-e2e: \
	$(TMPDIR)/.ci-operator-kubeconfig \
	$(TMPDIR)/hive-kubeconfig \
	$(TMPDIR)/sa.hive.hive.token.txt \
	$(TMPDIR)/local-secret/.dockerconfigjson \
	$(TMPDIR)/remote-secret/.dockerconfigjson \
	$(TMPDIR)/manifest-tool-secret/.dockerconfigjson \
	$(TMPDIR)/gcs/service-account.json \
	$(TMPDIR)/boskos \
	$(TMPDIR)/prometheus \
	$(TMPDIR)/promtool
	$(eval export KUBECONFIG=$(TMPDIR)/.ci-operator-kubeconfig)
	$(eval export HIVE_KUBECONFIG=$(TMPDIR)/hive-kubeconfig)
	$(eval export LOCAL_REGISTRY_SECRET_DIR=$(TMPDIR)/local-secret)
	$(eval export REMOTE_REGISTRY_SECRET_DIR=$(TMPDIR)/remote-secret)
	$(eval export GCS_CREDENTIALS_FILE=$(TMPDIR)/gcs/service-account.json)
	$(eval export MANIFEST_TOOL_SECRET=$(TMPDIR)/manifest-tool-secret/.dockerconfigjson)
	$(eval export LOCAL_REGISTRY_DNS=$(shell oc --context $(CLUSTER) get route -n openshift-image-registry -o jsonpath='{.items[0].spec.host}'))
	$(eval export PATH=${PATH}:$(TMPDIR))
	@$(MAKE) e2e
.PHONY: local-e2e

# Update golden output files for integration tests.
#
# Example:
#   make update-integration
#   make update-integration SUITE=multi-stage
update-integration:
	go run ./cmd/determinize-ci-operator --config-dir test/integration/ci-operator-config-mirror/input --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/ci-operator-config-mirror/input-to-clean --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/ci-operator-config-mirror/output --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/ci-operator-config-mirror/output-only-super --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/ci-operator-prowgen/input/config --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/config-brancher/expected --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/config-brancher/input --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/pj-rehearse/candidate/ci-operator/config --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/pj-rehearse/master/ci-operator/config --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/repo-init/expected/ci-operator/config --confirm
	go run ./cmd/determinize-ci-operator --config-dir test/integration/repo-init/input/ci-operator/config --confirm
	go run ./cmd/determinize-prow-config -prow-config-dir test/integration/repo-init/input/core-services/prow/02_config -sharded-plugin-config-base-dir test/integration/repo-init/input/core-services/prow/02_config
	go run ./cmd/determinize-prow-config -prow-config-dir test/integration/repo-init/expected/core-services/prow/02_config -sharded-plugin-config-base-dir test/integration/repo-init/expected/core-services/prow/02_config
	UPDATE=true make integration
.PHONY: update-integration

kubeExport := "jq 'del(.metadata.namespace,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp)'"

pr-deploy-configresolver:
	$(eval USER=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.user.login))
	$(eval BRANCH=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.ref))
	oc --context app.ci --as system:admin process -p USER=$(USER) -p BRANCH=$(BRANCH) -p PULL_REQUEST=$(PULL_REQUEST) -f hack/pr-deploy.yaml | oc  --context app.ci --as system:admin apply -f -
	echo "server is at https://$$( oc  --context app.ci --as system:admin get route server -n ci-tools-$(PULL_REQUEST) -o jsonpath={.spec.host} )"
.PHONY: pr-deploy

pr-deploy-backporter:
	$(eval USER=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.user.login))
	$(eval BRANCH=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.ref))
	oc --context app.ci --as system:admin process -p USER=$(USER) -p BRANCH=$(BRANCH) -p PULL_REQUEST=$(PULL_REQUEST) -f hack/pr-deploy-backporter.yaml | oc  --context app.ci --as system:admin apply -f -
	oc  --context app.ci --as system:admin get configmap plugins -n ci -o json | eval $(kubeExport) | oc  --context app.ci --as system:admin create -f - -n ci-tools-$(PULL_REQUEST)
	oc  --context app.ci --as system:admin get secret bugzilla-credentials-openshift-bugzilla-robot -n ci -o json | eval $(kubeExport) | oc  --context app.ci --as system:admin create -f - -n ci-tools-$(PULL_REQUEST)
	echo "server is at https://$$( oc  --context app.ci --as system:admin get route bp-server -n ci-tools-$(PULL_REQUEST) -o jsonpath={.spec.host} )"
.PHONY: pr-deploy-backporter

pr-deploy-vault-secret-manager:
	$(eval USER=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.user.login))
	$(eval BRANCH=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.ref))
	oc --context app.ci --as system:admin process -p USER=$(USER) -p BRANCH=$(BRANCH) -p PULL_REQUEST=$(PULL_REQUEST) -f hack/pr-deploy-vault-secret-manager.yaml | oc  --context app.ci --as system:admin apply -f -
	kubectl patch  -n vault rolebinding registry-viewer --type=json --patch='[{"op":"replace", "path":"/subjects/1/namespace", "value":"ci-tools-$(PULL_REQUEST)"}]'
	echo "server is at https://$$( oc  --context app.ci --as system:admin get route vault-secret-collection-manager -n ci-tools-$(PULL_REQUEST) -o jsonpath={.spec.host} )"
.PHONY: pr-deploy-backporter

pr-deploy-repo-init-api:
	$(eval USER=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.user.login))
	$(eval BRANCH=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.ref))
	$(eval GH_TOKEN=$(shell oc --context app.ci get secret -n ci github-credentials-openshift-bot -o json | jq -r '.data.oauth' | base64 -d))
	oc --context app.ci --as system:admin process -p USER=$(USER) -p BRANCH=$(BRANCH) -p PULL_REQUEST=$(PULL_REQUEST) -p GH_TOKEN=$(GH_TOKEN) -f hack/pr-deploy-repo-init-api.yaml | oc  --context app.ci --as system:admin apply -f -
	echo "server is at https://$$( oc  --context app.ci --as system:admin get route repo-init-api -n ci-tools-$(PULL_REQUEST) -o jsonpath={.spec.host} )"
.PHONY: pr-deploy-repo-init-api

pr-deploy-repo-init-ui:
	$(eval USER=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.user.login))
	$(eval BRANCH=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.ref))
	$(eval API_HOST=$(shell oc  --context app.ci --as system:admin get route repo-init-api -n ci-tools-$(PULL_REQUEST) -o jsonpath={.spec.host} ))
	oc --context app.ci --as system:admin process -p USER=$(USER) -p BRANCH=$(BRANCH) -p PULL_REQUEST=$(PULL_REQUEST) -p API_HOST=$(API_HOST) -f hack/pr-deploy-repo-init-ui.yaml | oc  --context app.ci --as system:admin apply -f -
	echo "server is at https://$$( oc  --context app.ci --as system:admin get route repo-init-ui -n ci-tools-$(PULL_REQUEST) -o jsonpath={.spec.host} )"
.PHONY: pr-deploy-repo-init-ui

pr-deploy-prpqr-ui:
	$(eval USER=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.user.login))
	$(eval BRANCH=$(shell curl --fail -Ss https://api.github.com/repos/openshift/ci-tools/pulls/$(PULL_REQUEST)|jq -r .head.ref))
	oc --context app.ci --as system:admin process -p USER=$(USER) -p BRANCH=$(BRANCH) -p PULL_REQUEST=$(PULL_REQUEST) -f hack/pr-deploy-prpqr-ui.yaml | oc  --context app.ci --as system:admin apply -f -
	oc --context app.ci --as system:admin start-build -n ci-tools-$(PULL_REQUEST) binaries
	echo "server is at https://$$( oc  --context app.ci --as system:admin get route payload-testing-ui -n ci-tools-$(PULL_REQUEST) -o jsonpath={.spec.host} )/runs"
.PHONY: pr-deploy-prpqr-ui


check-breaking-changes:
	test/validate-generation-breaking-changes.sh
.PHONY: check-breaking-changes

.PHONY: generate
generate: imports
	hack/update-codegen.sh
	hack/generate-ci-op-reference.sh

.PHONY: imports
imports:
	go run ./vendor/github.com/openshift-eng/openshift-goimports/ -m github.com/openshift/ci-tools

.PHONY: verify-gen
verify-gen: generate cmd/pod-scaler/frontend/dist/dummy cmd/repo-init/frontend/dist/dummy # we need the dummy file to exist so there's no diff on it
	@# Don't add --quiet here, it disables --exit code in the git 1.7 we have in CI, making this unusuable
	if  ! git diff --exit-code; then \
		echo "generated files are out of date, run make generate"; exit 1; \
	fi

update-unit:
	UPDATE=true go test ./...
.PHONY: update-unit

# requires github.com/golang/mock/mockgen
update-mocks:
	hack/update-mocks.sh
.PHONY: update-mocks

validate-registry-metadata:
	generate-registry-metadata -registry test/multistage-registry/registry
	git status -s ./test/multistage-registry/registry
	test -z "$$(git status -s ./test/multistage-registry/registry | grep registry)"
.PHONY: validate-registry-metadata

validate-checkconfig:
	test/validate-checkconfig.sh
.PHONY: validate-checkconfig

$(TMPDIR)/.ci-operator-kubeconfig:
	oc --context $(CLUSTER) -n test-credentials extract secret/ci-operator --keys kubeconfig --keys sa.ci-operator.$(CLUSTER).token.txt --to $(TMPDIR)
	mv $(TMPDIR)/kubeconfig "$@"

$(TMPDIR)/hive-kubeconfig:
	oc --context $(CLUSTER) --as system:admin --namespace test-credentials get secret hive-hive-credentials -o 'jsonpath={.data.kubeconfig}' | base64 --decode > "$@"

$(TMPDIR)/sa.hive.hive.token.txt:
	oc --context $(CLUSTER) --namespace test-credentials extract secret/hive-hive-credentials --keys sa.hive.hive.token.txt --to $(TMPDIR)

$(TMPDIR)/local-secret/.dockerconfigjson:
	mkdir -p $(TMPDIR)/local-secret
	oc --context $(CLUSTER) --as system:admin --namespace test-credentials get secret registry-pull-credentials -o 'jsonpath={.data.\.dockerconfigjson}' | base64 --decode | jq > $(TMPDIR)/local-secret/.dockerconfigjson

$(TMPDIR)/remote-secret/.dockerconfigjson:
	mkdir -p $(TMPDIR)/remote-secret
	oc --context $(CLUSTER) --as system:admin --namespace test-credentials get secret ci-pull-credentials -o 'jsonpath={.data.\.dockerconfigjson}' | base64 --decode | jq > $(TMPDIR)/remote-secret/.dockerconfigjson

$(TMPDIR)/gcs/service-account.json:
	mkdir -p $(TMPDIR)/gcs
	oc --context $(CLUSTER) --as system:admin --namespace test-credentials get secret gce-sa-credentials-gcs-publisher -o 'jsonpath={.data.service-account\.json}' | base64 --decode | jq > $(TMPDIR)/gcs/service-account.json

$(TMPDIR)/boskos:
	mkdir -p $(TMPDIR)/image
	oc image extract registry.ci.openshift.org/ci/boskos:latest --path /:$(TMPDIR)/image
	mv $(TMPDIR)/image/app $(TMPDIR)/boskos
	chmod +x $(TMPDIR)/boskos
	rm -rf $(TMPDIR)/image

$(TMPDIR)/manifest-tool-secret/.dockerconfigjson:
	mkdir -p $(TMPDIR)/manifest-tool-secret
	oc --context $(CLUSTER) --as system:admin --namespace test-credentials get secret manifest-tool-local-pusher -o 'jsonpath={.data.\.dockerconfigjson}' | base64 --decode | jq > $(TMPDIR)/manifest-tool-secret/.dockerconfigjson

local-pod-scaler: $(TMPDIR)/prometheus $(TMPDIR)/promtool cmd/pod-scaler/frontend/dist
	$(eval export PATH=${PATH}:$(TMPDIR))
	go run -tags e2e,e2e_framework ./test/e2e/pod-scaler/local/main.go
.PHONY: local-pod-scaler

.PHONY: cmd/pod-scaler/frontend/dist
cmd/pod-scaler/frontend/dist: cmd/pod-scaler/frontend/node_modules
	@$(MAKE) npm-pod-scaler NPM_ARGS="run build"
	@$(MAKE) cmd/pod-scaler/frontend/dist/dummy

local-pod-scaler-ui: cmd/pod-scaler/frontend/node_modules $(HOME)/.cache/pod-scaler/steps/container_memory_working_set_bytes.json
	go run -tags e2e,e2e_framework ./test/e2e/pod-scaler/local/main.go --cache-dir $(HOME)/.cache/pod-scaler --serve-dev-ui
.PHONY: local-pod-scaler-ui

$(HOME)/.cache/pod-scaler/steps/container_memory_working_set_bytes.json:
	mkdir -p $(HOME)/.cache/pod-scaler
	gsutil -m cp -r gs://origin-ci-resource-usage-data/* $(HOME)/.cache/pod-scaler

frontend-checks: cmd/pod-scaler/frontend/node_modules cmd/repo-init/frontend/node_modules
	@$(MAKE) npm-pod-scaler NPM_ARGS="run ci-checks"
	@$(MAKE) npm-repo-init NPM_ARGS="run ci-checks"
.PHONY: frontend-checks

cmd/pod-scaler/frontend/node_modules:
	@$(MAKE) npm-pod-scaler NPM_ARGS="ci"

cmd/pod-scaler/frontend/dist/dummy:
	echo "file used to keep go embed happy" > cmd/pod-scaler/frontend/dist/dummy

.PHONY: frontend-format
frontend-format: cmd/pod-scaler/frontend/node_modules cmd/repo-init/frontend/node_modules
	@$(MAKE) npm-pod-scaler  NPM_ARGS="run format"
	@$(MAKE) npm-repo-init  NPM_ARGS="run format"

.PHONY: cmd/repo-init/frontend/dist
cmd/repo-init/frontend/dist: cmd/repo-init/frontend/node_modules
	# This environment variable needs to be present when running the npm build as this is when it will get injected into the production artifact.
	# Ideally it would not be set here in the Make target, but doing this temporarily until something better is figured out.
	$(eval export REACT_APP_API_URI=https://repo-init-apiserver-ci.apps.ci.l2s4.p1.openshiftapps.com/api)
	@$(MAKE) npm-repo-init  NPM_ARGS="run build"
	@$(MAKE) cmd/repo-init/frontend/dist/dummy

cmd/repo-init/frontend/node_modules:
	@$(MAKE) npm-repo-init NPM_ARGS="ci"

cmd/repo-init/frontend/dist/dummy:
	echo "file used to keep go embed happy" > cmd/repo-init/frontend/dist/dummy

ifdef CI
NPM_FLAGS = 'npm_config_cache=/go/.npm'
endif

.PHONY: npm-pod-scaler
npm-pod-scaler:
	@$(MAKE) npm NPM_PREFIX="--prefix cmd/pod-scaler/frontend"

.PHONY: npm-repo-init
npm-repo-init:
	@$(MAKE) npm NPM_PREFIX="--prefix cmd/repo-init/frontend"

.PHONY: npm
npm:
	npm version
	env $(NPM_FLAGS) npm $(NPM_PREFIX) $(NPM_ARGS)

.PHONY: verify-frontend-format
verify-frontend-format: frontend-format
	@# Don't add --quiet here, it disables --exit code in the git 1.7 we have in CI, making this unusuable
	if  ! git diff --exit-code cmd/pod-scaler/frontend; then \
		echo "frontend files are not formatted, run make frontend-format"; exit 1; \
	fi

$(TMPDIR)/prometheus:
	mkdir -p $(TMPDIR)/image
	oc image extract quay.io/prometheus/prometheus:latest --path /bin/prometheus:$(TMPDIR)/image
	mv $(TMPDIR)/image/prometheus $(TMPDIR)/prometheus
	chmod +x $(TMPDIR)/prometheus
	rm -rf $(TMPDIR)/image

$(TMPDIR)/promtool:
	mkdir -p $(TMPDIR)/image
	oc image extract quay.io/prometheus/prometheus:main --path /bin/promtool:$(TMPDIR)/image
	mv $(TMPDIR)/image/promtool $(TMPDIR)/promtool
	chmod +x $(TMPDIR)/promtool
	rm -rf $(TMPDIR)/image

$(TMPDIR)/.promoted-image-governor-kubeconfig-dir:
	rm -rf $(TMPDIR)/.promoted-image-governor-kubeconfig-dir
	mkdir -p $(TMPDIR)/.promoted-image-governor-kubeconfig-dir
	oc --context app.ci --namespace ci extract secret/promoted-image-governor --confirm --to=$(TMPDIR)/.promoted-image-governor-kubeconfig-dir
	mkdir -p $(TMPDIR)/.config-updater-kubeconfig-dir
	oc --context app.ci extract secret/config-updater -n ci --to=$(TMPDIR)/.config-updater-kubeconfig-dir --confirm
	images/ci-secret-generator/oc_sa_create_kubeconfig.sh $(TMPDIR)/.config-updater-kubeconfig-dir app.ci promoted-image-governor ci > $(TMPDIR)/.promoted-image-governor-kubeconfig-dir/sa.promoted-image-governor.app.ci.config

release_folder := $$PWD/../release

promoted-image-governor: $(TMPDIR)/.promoted-image-governor-kubeconfig-dir
	go run  ./cmd/promoted-image-governor --kubeconfig-dir=$(TMPDIR)/.promoted-image-governor-kubeconfig-dir --ci-operator-config-path=$(release_folder)/ci-operator/config --release-controller-mirror-config-dir=$(release_folder)/core-services/release-controller/_releases --ignored-image-stream-tags='^ocp\S*/\S+:machine-os-content$$' --ignored-image-stream-tags='^openshift/origin-v3.11:' --dry-run=true
.PHONY: promoted-image-governor

explain: $(TMPDIR)/.promoted-image-governor-kubeconfig-dir
	@[[ $$istag ]] || (echo "ERROR: \$$istag must be set"; exit 1)
	@go run  ./cmd/promoted-image-governor --kubeconfig-dir=$(TMPDIR)/.promoted-image-governor-kubeconfig-dir --kubeconfig-suffix=config --ci-operator-config-path=$(release_folder)/ci-operator/config --release-controller-mirror-config-dir=$(release_folder)/core-services/release-controller/_releases --explain $(istag) --dry-run=true --log-level=fatal
.PHONY: explain


$(TMPDIR)/.github-ldap-user-group-creator-kubeconfig-dir:
	rm -rf $(TMPDIR)/.github-ldap-user-group-creator-kubeconfig-dir
	mkdir -p $(TMPDIR)/.github-ldap-user-group-creator-kubeconfig-dir
	oc --context app.ci --namespace ci extract secret/github-ldap-user-group-creator --confirm --to=$(TMPDIR)/.github-ldap-user-group-creator-kubeconfig-dir
	oc --context app.ci --namespace ci serviceaccounts create-kubeconfig github-ldap-user-group-creator | sed 's/github-ldap-user-group-creator/app.ci/g' > $(TMPDIR)/.github-ldap-user-group-creator-kubeconfig-dir/sa.github-ldap-user-group-creator.app.ci.config

github-ldap-user-group-creator: $(TMPDIR)/.github-ldap-user-group-creator-kubeconfig-dir
	go run  ./cmd/github-ldap-user-group-creator --kubeconfig-dir=$(TMPDIR)/.github-ldap-user-group-creator-kubeconfig-dir --groups-file=/tmp/groups.yaml --mapping-file=/tmp/mapping.yaml --config-file=$(release_folder)/core-services/sync-rover-groups/_config.yaml --dry-run=true --log-level=debug
.PHONY: github-ldap-user-group-creator

sync-rover-groups:
	go run  ./cmd/sync-rover-groups --manifest-dir=$(release_folder)/clusters --config-file=$(release_folder)/core-services/sync-rover-groups/_config.yaml --mapping-file=/tmp/mapping.yaml --github-users-file=/tmp/users.yaml --log-level=debug
.PHONY: sync-rover-groups

$(TMPDIR)/.cluster-display-kubeconfig-dir:
	rm -rf $(TMPDIR)/.cluster-display-kubeconfig-dir
	mkdir -p $(TMPDIR)/.cluster-display-kubeconfig-dir
	oc --context app.ci --namespace ci extract secret/cluster-display --confirm --to=$(TMPDIR)/.cluster-display-kubeconfig-dir

cluster-display: $(TMPDIR)/.cluster-display-kubeconfig-dir
	@go run  ./cmd/cluster-display --kubeconfig-dir=$(TMPDIR)/.cluster-display-kubeconfig-dir --kubeconfig-suffix=config
.PHONY: cluster-display

analyse-deps: cmd/vault-secret-collection-manager/index.js
	@snyk test --project-name=ci-tools --org=red-hat-org
.PHONY: analyse-deps

ARTIFACTS ?= "."

analyse-code:
	@snyk code test --project-name=ci-tools --org=red-hat-org --sarif --sarif-file-output=${ARTIFACTS}/snyk.sarif.json > /dev/null || true

	@echo The following vulnerabilities fingerprints are found:
	@jq -r '.runs[].results[].fingerprints[]' ${ARTIFACTS}/snyk.sarif.json | awk 'NR==FNR { b[$$0] = 1; next } !b[$$0]' .snyk-ignore -

	@echo Full vulnerabilities report is available at ${ARTIFACTS}/snyk.sarif.json

.PHONY: analyse-code

build-and-push-image:
	hack/build-and-push.sh "${TOOL}" "${QUAY_ACCOUNT}"
.PHONY: build-and-push-image
