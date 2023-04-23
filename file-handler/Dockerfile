FROM golang:alpine AS builder
RUN apk update && apk add --no-cache git
WORKDIR /app
COPY ./file-handler .
RUN go mod init github.com/leonardopoggiani/file-handler
RUN go mod tidy
RUN go build -o file-handler .

FROM alpine
COPY --from=builder /app/file-handler .
CMD ["./file-handler"]
