# Trusted local fixture only. These capabilities are not a sandbox for untrusted code.
FROM docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489 AS engine
FROM debian:bookworm-20261020-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
ENV container=docker
RUN printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d \
 && chmod 0755 /usr/sbin/policy-rc.d \
 && apt-get update \
 && apt-get install -y --no-install-recommends systemd=252.39-1~deb12u2 openssh-server=1:9.2p1-2+deb12u10 openssh-client=1:9.2p1-2+deb12u10 socat=1.7.4.4-2 \
 && dpkg-query -W > /fixture-packages.txt \
 && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_* \
 && truncate -s 0 /etc/machine-id \
 && usermod --password '*' root \
 && mkdir -p /fixture /root/.ssh /etc/systemd/system/multi-user.target.wants
COPY --from=engine /usr/local/bin/docker /usr/local/bin/dockerd /usr/local/bin/containerd /usr/local/bin/containerd-shim-runc-v2 /usr/local/bin/ctr /usr/local/bin/runc /usr/local/bin/docker-init /usr/local/bin/
COPY --from=engine /usr/local/libexec/docker/cli-plugins/docker-compose /fixture/compose-plugin
RUN printf '%s\n' 'Port 22' 'HostKey /fixture/host' 'AuthorizedKeysFile /root/.ssh/authorized_keys' 'PermitRootLogin prohibit-password' 'PasswordAuthentication no' 'KbdInteractiveAuthentication no' 'UsePAM no' 'DisableForwarding yes' 'PermitUserEnvironment no' 'LogLevel VERBOSE' > /etc/ssh/sshd_config \
 && printf '%s\n' '[Unit]' 'Description=Private fixture Docker engine' '[Service]' 'ExecStart=/usr/local/bin/dockerd --host=unix:///run/docker.sock --storage-driver=vfs --bridge=none --userland-proxy=false --iptables=false --ip6tables=false --ip-forward=false' 'Delegate=yes' 'KillMode=process' '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/fixture-docker.service \
 && printf '%s\n' '[Unit]' 'Description=Fixed SSH qualification marker' '[Service]' 'Type=oneshot' 'RemainAfterExit=yes' 'ExecStart=/bin/sh -c "echo fixture-marker >>/fixture/marker"' > /etc/systemd/system/fixture-marker.service \
 && printf '%s\n' '[Unit]' 'Description=Unrelated fixture sentinel' '[Service]' 'ExecStart=/bin/sleep infinity' '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/fixture-sentinel.service \
 && ln -sf /lib/systemd/system/ssh.service /etc/systemd/system/multi-user.target.wants/ssh.service \
 && ln -s ../fixture-docker.service /etc/systemd/system/multi-user.target.wants/fixture-docker.service \
 && ln -s ../fixture-sentinel.service /etc/systemd/system/multi-user.target.wants/fixture-sentinel.service \
 && printf 'unrelated fixture sentinel\n' >/fixture/sentinel
COPY entrypoint.sh /fixture/entrypoint.sh
STOPSIGNAL SIGRTMIN+3
ENTRYPOINT ["/bin/bash", "/fixture/entrypoint.sh"]
