# Build the manager binary
FROM docker.io/golang:latest as builder

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum

RUN go mod download

COPY . ./

RUN apt-get update && \
    apt-get install ca-certificates && \
    update-ca-certificates && \
    rm -rf /var/cache/apk/*

RUN CGO_ENABLED=0 go build -o live-migrating-operator .


FROM gcr.io/distroless/static-debian11:nonroot
WORKDIR /
COPY --from=builder /workspace .
USER 65532:65532

ENTRYPOINT ["/manager"]
