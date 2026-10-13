# Builds the wallet-api binary and ships it on a minimal, non-root image.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wallet-api ./cmd/wallet-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wallet-api /wallet-api
EXPOSE 8080
ENTRYPOINT ["/wallet-api"]
