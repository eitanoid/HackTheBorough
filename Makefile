.PHONY: check-skaffold cluster-up setup deploy dev delete teardown down

check-skaffold:
	@command -v skaffold >/dev/null 2>&1 || { \
		echo "Skaffold is required but was not found on PATH." >&2; \
		echo "Install it from https://skaffold.dev/docs/install/ and retry." >&2; \
		exit 1; \
	}

cluster-up:
	bash dev/up.sh

# Create the cluster and install both services once.
setup: check-skaffold cluster-up
	skaffold run

deploy: setup

# Run Skaffold's file sync / hot-reload loop until interrupted.
dev: check-skaffold cluster-up
	skaffold dev

delete: check-skaffold
	skaffold delete

# Remove the whole local cluster and everything deployed into it.
teardown:
	bash dev/down.sh

down: teardown
