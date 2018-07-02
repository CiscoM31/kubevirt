export GO15VENDOREXPERIMENT := 1

all: build manifests

generate:
	hack/dockerized "KUBEVIRT_PROVIDER=${KUBEVIRT_PROVIDER} DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} ./hack/generate.sh"

apidocs: generate
	./hack/gen-swagger-doc/gen-swagger-docs.sh v1 html"

client-python: generate
	TRAVIS_TAG=${TRAVIS_TAG} ./hack/gen-client-python/generate.sh"

build:
	hack/dockerized "./hack/check.sh && KUBEVIRT_VERSION=${KUBEVIRT_VERSION} ./hack/build-go.sh install ${WHAT}" && ./hack/build-copy-artifacts.sh ${WHAT}

goveralls:
	SYNC_OUT=false hack/dockerized "./hack/check.sh && TRAVIS_JOB_ID=${TRAVIS_JOB_ID} TRAVIS_PULL_REQUEST=${TRAVIS_PULL_REQUEST} TRAVIS_BRANCH=${TRAVIS_BRANCH} ./hack/goveralls.sh"

test:
	SYNC_OUT=false hack/dockerized "./hack/check.sh && ./hack/build-go.sh test ${WHAT}"

functest:
	hack/dockerized "hack/build-func-tests.sh"
	hack/functests.sh

clean:
	hack/dockerized "./hack/build-go.sh clean ${WHAT} && rm _out/* -rf"
	rm -f tools/openapispec/openapispec tools/crd-generator/crd-generator tools/manifest-templator/manifests-templator tools/vms-generator/vms-generator

distclean: clean
	hack/dockerized "rm -rf vendor/ && rm -f .glide.*.hash && glide cc"
	rm -rf vendor/

deps-install:
	SYNC_VENDOR=true hack/dockerized "glide install --strip-vendor"
	hack/dep-prune.sh

deps-update:
	SYNC_VENDOR=true hack/dockerized "glide cc && glide update --strip-vendor"
	hack/dep-prune.sh

docker: build
	hack/build-docker.sh build ${WHAT}

publish: docker
	hack/build-docker.sh push ${WHAT}

manifests:
	hack/dockerized "DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} IMAGE_PULL_POLICY=${IMAGE_PULL_POLICY} ./hack/build-manifests.sh"

.release-functest:
	make functest > .release-functest 2>&1

release-announce: .release-functest
	./hack/release-announce.sh $(RELREF) $(PREREF)

cluster-up:
	./cluster/up.sh

cluster-down:
	./cluster/down.sh

cluster-build: manifests build publish
	./cluster/build.sh

cluster-clean:
	./cluster/clean.sh

cluster-deploy: cluster-clean
	./cluster/deploy.sh

cluster-sync: cluster-build cluster-deploy

.PHONY: build test clean distclean checksync sync docker manifests publish functest release-announce cluster-up cluster-down cluster-clean cluster-deploy cluster-sync
