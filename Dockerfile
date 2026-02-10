FROM golang:1.23-alpine AS build
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /zenithpay-retry ./cmd/server

FROM alpine:3.20
RUN apk --no-cache add ca-certificates && \
    adduser -D -u 1000 appuser
USER appuser
COPY --from=build /zenithpay-retry /zenithpay-retry
EXPOSE 8080
CMD ["/zenithpay-retry"]
