# ---- Build Stage ----
FROM golang:latest AS build-stage

ARG GITHUB_TOKEN

# Set the working directory
WORKDIR /app

# Install necessary dependencies
RUN apt update && \
    apt install -y libbtrfs-dev libdevmapper-dev libgpgme-dev libseccomp-dev && \
    apt clean && \
    rm -rf /var/lib/apt/lists/*

# Copy only necessary files
# Adjust this as needed, for instance, if you have go mod files, copy them first
COPY go.mod go.sum ./
RUN go mod download

COPY ./api-server ./api-server

# Set up private repo access and build
RUN echo "machine github.com login $GITHUB_TOKEN" > ~/.netrc && \
    chmod 600 ~/.netrc && \
    go env -w GOPRIVATE=github.com/leonardopoggiani/* && \
    go build -mod=readonly -o live-migration-operator ./api-server/cmd/main.go

# Wipe the token after use
RUN rm ~/.netrc

# ---- Runtime Stage ----
FROM debian:bullseye-slim

WORKDIR /app

# Copy binary from build stage
COPY --from=build-stage /app/live-migration-operator /app/

# Start the API server
CMD ["./live-migration-operator"]
