# Build the manager binary
FROM docker.io/ubuntu:latest as builder

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum

COPY . ./

RUN apt-get update && \
    apt-get install ca-certificates -y && \
    update-ca-certificates && \
    apt-get install libbtrfs-dev libdevmapper-dev gpg golang-github-proglottis-gpgme-dev libgpgme11 libgpgmepp-dev wget -y && \
    wget https://go.dev/dl/go1.20.2.linux-amd64.tar.gz && \
    rm -rf /usr/local/go && tar -C /usr/local -xzf go1.20.2.linux-amd64.tar.gz && \
    export PATH=$PATH:/usr/local/go/bin && \
    go get github.com/containers/buildah && \
    go mod download && \
    rm -rf /var/cache/apk/* && \
    go build -o live-migrating-operator ./api-server/cmd/main.go

FROM gcr.io/distroless/static-debian11:nonroot
WORKDIR /
COPY --from=builder /workspace .
USER 65532:65532

ENTRYPOINT ["/manager"]
