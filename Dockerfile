# Build the manager binary
FROM golang:1.17 as builder

WORKDIR /workspace

# Copy the go source
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -mod vendor -a -o manager main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot-arm64
WORKDIR /
COPY --from=builder /workspace/manager .
COPY bindata ./bindata
USER 65532:65532

ENTRYPOINT ["/manager"]
