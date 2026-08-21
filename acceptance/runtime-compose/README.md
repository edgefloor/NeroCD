# Compose runtime gate fixture

This fixture mounts the Docker daemon socket into the non-root `compose-deploy`
runner with only its credential, journal, workspace, and declared secret
volumes. Docker-socket access is root-equivalent on the host; this acceptance
gate must run only on a dedicated disposable VM/daemon. The socket is used to
exercise the real typed Compose adapter, not as a production isolation claim.
