.PHONY: cluster-up setup deploy dev delete teardown down generate test test-integration check-env

cluster-up: check-env
	./dev/up.sh

# Create the cluster and install both services once.
setup: cluster-up
	skaffold run

deploy: setup

# Run Skaffold's file sync / hot-reload loop until interrupted.
dev: cluster-up
	skaffold dev

delete:
	skaffold delete

# Remove the whole local cluster and everything deployed into it.
teardown:
	./dev/down.sh

down: teardown

# Generate protobuf, Connect-Go, and TypeScript stubs.
generate:
	buf lint
	buf generate

# Unit tests. Postgres integration tests are skipped without TEST_DATABASE_URL.
test:
	cd svc/backend && go test ./...

# Postgres integration tests. Export TEST_DATABASE_URL first.
test-integration:
	@test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL is required" && exit 1)
	cd svc/backend && go test -p 1 ./...


check-env:
ifndef OPENAPI_KEY
	$(error OPENAPI_KEY is undefined)
endif
