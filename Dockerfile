ARG BASE_IMAGE=scratch
ARG ARTEFACTS_IMAGE=alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412
# tz + certs stage
# Extract CA bundle and zoneinfo in separate stages to keep final image minimal
FROM ${ARTEFACTS_IMAGE} AS alpine-artefacts
RUN apk add --no-cache tzdata ca-certificates zip
RUN mkdir -p /tmp/zoneinfo && cp -r /usr/share/zoneinfo /tmp/zoneinfo \
    && cd /tmp && zip -r /zoneinfo.zip zoneinfo \
    && rm -rf /tmp/zoneinfo \
    && mkdir -p /tmp/certs \
    && cp /etc/ssl/certs/ca-certificates.crt /tmp/certs/ca-certificates.crt

FROM ${ARTEFACTS_IMAGE} AS bin
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
  && cp /dist/${BINARY}_${TARGETOS}_${TARGETARCH}${SUFFIX}/${BINARY} /dist/${BINARY}

FROM ${BASE_IMAGE} AS final
ARG BINARY
ENV BINNAME=${BINARY}

COPY --from=alpine-artefacts /zoneinfo.zip /zoneinfo.zip
COPY --from=alpine-artefacts /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV ZONEINFO=/zoneinfo.zip

COPY --from=bin /dist/${BINNAME} /usr/bin/entrypoint

ENTRYPOINT ["/usr/bin/entrypoint"]
