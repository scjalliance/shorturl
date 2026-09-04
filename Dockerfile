# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/shorturl ./cmd/shorturl

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shorturl /shorturl
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/shorturl"]
