FROM alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412 AS bin

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG BINARY

COPY dist/ /dist
RUN set -ex \
  && case "${TARGETARCH}" in \
      amd64) \
          SUFFIX="_v1" \
          ;; \
      arm) \
          SUFFIX="_${TARGETVARIANT:1}" \
          ;; \
      *) \
          SUFFIX="" \
          ;; \
    esac \
  && cp /dist/${BINARY}_${TARGETOS}_${TARGETARCH}${SUFFIX}/${BINARY} /dist/entrypoint;

FROM cgr.dev/chainguard/go AS final
COPY --from=bin /dist/entrypoint /usr/bin/entrypoint
ENTRYPOINT ["/usr/bin/entrypoint"]