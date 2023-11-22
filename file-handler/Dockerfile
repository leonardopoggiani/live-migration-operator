FROM golang:alpine AS builder

RUN apk update && apk add --no-cache git

WORKDIR /app/
 
COPY ./file-handler.go ./

COPY go.* ./

RUN go mod download
RUN go build -o file-handler ./

FROM alpine

COPY --from=builder /app/file-handler ./

CMD ["./file-handler"]
