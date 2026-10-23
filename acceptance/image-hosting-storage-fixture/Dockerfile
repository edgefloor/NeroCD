FROM scratch
ARG FIXTURE_OWNER
LABEL io.nerocd.storage-runtime.owner=$FIXTURE_OWNER
COPY app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
