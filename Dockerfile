# Một image, nhiều binary: chọn bằng entrypoint (/app/redirect | /app/api | /app/consumer | /app/migrate | /app/apikey).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
USER nonroot
ENTRYPOINT ["/app/api"]
