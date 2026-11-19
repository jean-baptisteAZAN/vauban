# Throwaway "existing server" for the vauban end-to-end test: a Debian host
# with sshd, sudo and a Docker engine, run with --privileged by the test.
FROM debian:trixie-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server sudo docker.io docker-cli docker-compose util-linux ca-certificates iptables procps \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -s /bin/bash ops \
 && echo 'ops ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/ops \
 && chmod 440 /etc/sudoers.d/ops \
 && mkdir -p /run/sshd
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
