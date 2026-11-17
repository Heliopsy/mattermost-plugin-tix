# Include custom targets and environment variables here

## Runs the end-to-end suite: builds the bundle for this machine, starts tix and Mattermost in
## podman, seeds them, runs every scenario and tears everything down. E2E_KEEP=1 keeps pulled images.
.PHONY: e2e
e2e:
	$(MAKE) dist MM_SERVICESETTINGS_ENABLEDEVELOPER=true DEFAULT_GOOS=linux
	python3 e2e/run.py $(E2E_FLAGS) dist/$(BUNDLE_NAME)
