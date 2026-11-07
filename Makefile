.PHONY: cluster-up setup deploy dev delete teardown down

cluster-up:
	bash dev/up.sh

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
	bash dev/down.sh

down: teardown

# Generate gRPC stubs
generate:
