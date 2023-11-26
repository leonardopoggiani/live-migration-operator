FROM golang:1.21 AS build-stage
WORKDIR /app

RUN apt update && \
    apt install -y libbtrfs-dev libdevmapper-dev libgpgme-dev libseccomp-dev && \
    apt clean && \
    rm -rf /var/lib/apt/lists/*

COPY go.mod ./go.mod

COPY ./api ./api
COPY ./api-server ./api-server
COPY ./config ./config
COPY ./controllers ./controllers
COPY ./main.go ./main.go

RUN go mod tidy 
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags containers_image_openpgp -a -installsuffix cgo -o ./bin/live-migration-operator .

FROM gcr.io/distroless/static-debian12:latest
WORKDIR /app

COPY --from=build-stage /app/bin/live-migration-operator /app/live-migration-operator

CMD ["./live-migration-operator"]
