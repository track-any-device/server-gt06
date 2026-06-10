FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /gt06-server ./server/cmd

FROM gcr.io/distroless/static-debian12
COPY --from=builder /gt06-server /gt06-server
EXPOSE 7019 9091
ENTRYPOINT ["/gt06-server"]
