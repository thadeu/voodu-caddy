# The voodu-caddy image: Caddy with two modules that stock Caddy lacks.
#
#   caddy-l4               serves `tcp {}` ingresses: TCP with TLS, routed by SNI
#   caddy-dns/cloudflare   the DNS-01 challenge, for wildcard certificates
#
# The release workflow builds it for amd64 and arm64 and publishes it as
# ghcr.io/thadeu/voodu-caddy:<plugin version>, the default image of the
# install hook. Versions are pinned, so a rebuild is the same image.
ARG CADDY_VERSION=2.11

FROM caddy:${CADDY_VERSION}-builder AS builder

RUN xcaddy build \
  --with github.com/mholt/caddy-l4@v0.1.2 \
  --with github.com/caddy-dns/cloudflare@v0.2.4

FROM caddy:${CADDY_VERSION}

COPY --from=builder /usr/bin/caddy /usr/bin/caddy
