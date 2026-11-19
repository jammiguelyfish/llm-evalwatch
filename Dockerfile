FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /evalwatch ./cmd/evalwatch \
 && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /evalwatch /evalwatch
COPY suites /suites
# a named volume mounted here inherits this ownership, so the non-root user can write run history
COPY --from=build --chown=65532:65532 /data /data
USER nonroot
ENTRYPOINT ["/evalwatch"]
CMD ["serve", "-suite", "/suites/basics.json", "-data", "/data"]
