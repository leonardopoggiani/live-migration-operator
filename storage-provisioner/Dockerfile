# Step 1: Build the binary
FROM golang:1.20.2 AS builder

# Copy the code from the current directory to the container.
COPY . /app
COPY go.mod /app
COPY go.sum /app

# Set the working directory to the app directory.
WORKDIR /app

# Build the Go binary.
RUN go build -o checkpoint-provisioner .

# Step 2: Copy the binary to a smaller container
FROM alpine:3.17.2

# Copy the binary from the builder container.
COPY --from=builder /app/checkpoint-provisioner /app/checkpoint-provisioner

# Set the default command to run the binary.
CMD ["/app/checkpoint-provisioner"]
