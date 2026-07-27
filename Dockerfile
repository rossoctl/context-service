FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /context-service ./cmd/context-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /context-service /context-service
USER nonroot:nonroot
ENTRYPOINT ["/context-service"]
