FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /backend .

FROM alpine:3.23
RUN addgroup -S app && adduser -S -G app -u 10001 app && mkdir /data && chown app:app /data
COPY --from=build /backend /usr/local/bin/backend
USER app
ENV HTTP_ADDR=:8080 DATA_FILE=/data/warehouse.json
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/backend"]
