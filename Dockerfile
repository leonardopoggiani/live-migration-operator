# Use an official Golang runtime as a parent image
FROM golang:latest

ARG GITHUB_TOKEN

# Set the working directory to /app
WORKDIR /app

# Copy the Go file into the container
COPY . ./

RUN apt update && \
    apt install -y libbtrfs-dev libdevmapper-dev libgpgme-dev libseccomp-dev

RUN echo "machine github.com login $GITHUB_TOKEN" > ~/.netrc && \
    chmod 600 ~/.netrc

RUN go env -w GOPRIVATE=github.com/leonardopoggiani/* && \
    rm go.sum && \
    rm go.mod && \
    go clean -modcache && \
    go mod init github.com/leonardopoggiani/live-migration-operator && \
    go mod tidy

RUN go get -u github.com/leonardopoggiani/live-migration-operator && \
    go get -u github.com/leonardopoggiani/live-migration-operator/storage-provisioner && \
    go get -u github.com/Microsoft/hcsshim && \
    go get github.com/docker/distribution/metrics@v2.8.1+incompatible && \
    go get github.com/containers/buildah


RUN go build -mod=readonly -o live-migration-operator ./api-server/cmd/main.go

# Start the API server
CMD ["./live-migration-operator"]