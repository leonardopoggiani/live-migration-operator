# Build the manager binary
FROM docker.io/golang:latest as builder

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum

RUN go mod tidy

RUN go mod download

COPY . ./

RUN apt-get update && \
    apt-get install ca-certificates && \
    update-ca-certificates && \
    apt-get install libbtrfs-dev libdevmapper-dev -y gpg golang-github-proglottis-gpgme-dev libgpgme11 libgpgmepp-dev && \
    go mod download && \
    go get github.com/containers/buildah && \
    rm -rf /var/cache/apk/*

RUN CGO_ENABLED=0 go build -o live-migrating-operator ./api-server/cmd/main.go


FROM gcr.io/distroless/static-debian11:nonroot
WORKDIR /
COPY --from=builder /workspace .
USER 65532:65532

ENTRYPOINT ["/manager"]
