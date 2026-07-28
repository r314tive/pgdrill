ARG RUNTIME_IMAGE=debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

FROM scratch AS release
ARG ARCHIVE_VERSION
ARG TARGETARCH
ARG SOURCE_DATE_EPOCH
ADD dist/pgdrill_${ARCHIVE_VERSION}_linux_${TARGETARCH}.tar.gz /release/

FROM ${RUNTIME_IMAGE}
ARG ARCHIVE_VERSION
ARG TARGETARCH
ARG VERSION
ARG COMMIT
ARG CREATED
ARG SOURCE_DATE_EPOCH

LABEL org.opencontainers.image.title="pgdrill" \
      org.opencontainers.image.description="PostgreSQL recovery readiness engine" \
      org.opencontainers.image.url="https://github.com/r314tive/pgdrill" \
      org.opencontainers.image.source="https://github.com/r314tive/pgdrill" \
      org.opencontainers.image.documentation="https://github.com/r314tive/pgdrill/blob/main/docs/container-image.md" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.base.name="docker.io/library/debian:bookworm-slim" \
      org.opencontainers.image.base.digest="sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818"

COPY --from=release --chmod=0755 /release/pgdrill_${ARCHIVE_VERSION}_linux_${TARGETARCH}/pgdrill /usr/local/bin/pgdrill

ENV HOME=/tmp
WORKDIR /tmp
USER 65532:65532
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/pgdrill"]
