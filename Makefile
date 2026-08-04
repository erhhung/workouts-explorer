.PHONY: all generate check-generated check-ui-artifacts check-version fmt vet test test-ui build images publish-dev-images verify helm migration-test vcluster-test compose-up compose-down

SHORT_SHA ?= $(shell git rev-parse HEAD | cut -c1-8)
APP_VERSION := $(shell tr -d '\n' < VERSION)

all: verify

generate:
	go generate ./api

check-generated:
	./scripts/check-generated.sh

check-ui-artifacts:
	./scripts/check-ui-artifacts.sh

check-version:
	./scripts/check-version.sh

fmt:
	test -z "$$(gofmt -l api internal worker)"

vet:
	go vet ./...

test:
	go test ./...

test-ui:
	npm --prefix ui test

build:
	go build ./api/cmd/api ./api/cmd/migrate ./api/cmd/bootstrap-admin ./api/cmd/provision-roles ./worker/cmd/worker
	npm --prefix ui run build

images:
	./scripts/prune-workouts-images.sh
	buildah build --file api/Dockerfile --tag workouts-api:$(SHORT_SHA) --tag workouts-api:$(APP_VERSION) .
	buildah build --file worker/Dockerfile --tag workouts-worker:$(SHORT_SHA) --tag workouts-worker:$(APP_VERSION) .
	buildah build --file ui/Dockerfile --tag workouts-ui:$(SHORT_SHA) --tag workouts-ui:$(APP_VERSION) .

publish-dev-images:
	./scripts/publish-dev-images.sh
	./scripts/check-ui-artifacts.sh

helm:
	./scripts/check-helm.sh

verify: check-generated check-ui-artifacts check-version fmt vet test test-ui build helm

migration-test:
	./scripts/test-migrations.sh

vcluster-test:
	./scripts/test-vcluster.sh

compose-up:
	docker compose up -d --wait postgres

compose-down:
	docker compose down -v
