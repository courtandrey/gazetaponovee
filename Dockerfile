FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gazeta . \
 && mkdir /data && chown 65534:65534 /data

FROM scratch
COPY --from=build /gazeta /gazeta
COPY --from=build --chown=65534:65534 /data /data
USER 65534:65534
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/gazeta"]
