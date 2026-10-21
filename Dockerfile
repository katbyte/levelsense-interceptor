# build with the vendored deps, then ship the static binary on alpine: small, but keeps a shell
# so you can `docker exec -it levelsense-interceptor sh` to poke at things. runs as a non-root user.
# make docker passes VERSION/COMMIT from git; a bare `docker build .` reports "dev".
#
# REGISTRY is where the two base images come from: Google's mirror of Docker Hub, which holds
# the same images, unless a build says otherwise. Docker Hub itself (docker.io/library) limits
# how much an address may pull, which a CI runner's shared address runs into; CI builds again
# from it when the mirror gives nothing. Nothing here needs a newer Dockerfile syntax than every
# builder has, so no syntax image is named: that was a third thing to fetch before a build.
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24
ARG REGISTRY=mirror.gcr.io/library

FROM ${REGISTRY}/golang:${GO_VERSION}-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor \
      -ldflags "-s -w \
        -X github.com/katbyte/go-kt/version.Version=${VERSION} \
        -X github.com/katbyte/go-kt/version.GitCommit=${COMMIT}" \
      -o /levelsense-interceptor .

FROM ${REGISTRY}/alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 65532 interceptor
COPY --from=build /levelsense-interceptor /usr/local/bin/levelsense-interceptor
USER interceptor
# the freezer's requests are taken on 8080, above the ports only root may take: publish the
# host's port 80 to it, the only port the freezer sends to. the readings and the state for Home
# Assistant are served on 9125. the healthcheck assumes the default readings port.
ENV LEVELSENSE_INTERCEPTOR_LISTEN=:8080
ENV LEVELSENSE_INTERCEPTOR_METRICS_LISTEN=:9125
EXPOSE 8080 9125
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q --spider http://127.0.0.1:9125/healthz || exit 1
ENTRYPOINT ["levelsense-interceptor"]
CMD ["serve"]
