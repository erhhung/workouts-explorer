.PHONY: all generate check-generated check-ui-artifacts check-version check-dev-postgres \
				fmt vet test test-ui build helm verify images publish-dev-images osm-update \
				migration-test vcluster-test compose-up compose-down

SHORT_SHA ?= $(shell git rev-parse HEAD | cut -c1-8)
APP_VERSION := $(shell tr -d '\n' < VERSION)

all: verify

generate:
	@go generate ./api

check-generated:
	@./scripts/check-generated.sh

check-ui-artifacts:
	@./scripts/check-ui-artifacts.sh

check-version:
	@./scripts/check-version.sh

check-dev-postgres:
	@./scripts/check-dev-postgres.sh

fmt:
	@test -z "$$(gofmt -l api internal worker)"

vet:
	@go vet ./...

test:
	@go test ./...

test-ui:
	@npm --prefix ui test

build:
	@go build \
		./api/cmd/api \
		./api/cmd/migrate \
		./api/cmd/bootstrap-admin \
		./api/cmd/provision-roles \
		./worker/cmd/worker \
		./worker/cmd/coverage-worker \
		./worker/cmd/osm-migrate \
		./worker/cmd/osm-catalog \
		./worker/cmd/osm-update \
		./worker/cmd/osm-identity-eval \
		./worker/cmd/timezone-import \
		./worker/cmd/timezone-backfill \
		./worker/cmd/coverage-evaluate
	@npm --prefix ui run build

helm:
	@./scripts/check-helm.sh

verify: check-generated check-ui-artifacts check-version check-dev-postgres fmt vet test test-ui build helm

images:
	@./scripts/prune-workouts-images.sh

	@buildah build --file api/Dockerfile \
		--tag workouts-api:$(SHORT_SHA) \
		--tag workouts-api:$(APP_VERSION) .
	@buildah build --file ui/Dockerfile \
		--tag workouts-ui:$(SHORT_SHA) \
		--tag workouts-ui:$(APP_VERSION) .
	@buildah build --file worker/Dockerfile \
		--tag workouts-worker:$(SHORT_SHA) \
		--tag workouts-worker:$(APP_VERSION) .
	@buildah build --file worker/osm-update.Dockerfile \
		--tag workouts-osm:$(SHORT_SHA) \
		--tag workouts-osm:$(APP_VERSION) .

publish-dev-images:
	@./scripts/publish-dev-images.sh
	@./scripts/check-ui-artifacts.sh

osm-update:
	@./scripts/osm-update-operator.sh

migration-test:
	@./scripts/test-migrations.sh

vcluster-test:
	@./scripts/test-vcluster.sh

compose-up:
	@docker compose up -d --wait postgres

compose-down:
	@docker compose down -v
