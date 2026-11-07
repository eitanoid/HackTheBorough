.PHONY: cluster-up setup deploy dev delete teardown down generate

cluster-up:
	./dev/up.sh

# Create the cluster and install both services once.
setup: cluster-up
	skaffold dev

deploy: setup

# Run Skaffold's file sync / hot-reload loop until interrupted.
dev: cluster-up
	skaffold run

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
