# Use an official Golang runtime as a parent image
FROM golang:latest

# Set the working directory to /app
WORKDIR /app

# Copy the Go file into the container
COPY . ./

# Build the Go binary
RUN apt update && apt install libbtrfs-dev libgpgme-dev libdevmapper-dev -y && \
    go get github.com/containers/buildah

RUN go build -o live-migration-operator ./api-server/cmd/main.go

# Start the API server
CMD ["./api-server"]