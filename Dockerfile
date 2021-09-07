FROM golang:1.14.3-alpine AS build_base

RUN apk add --no-cache git

# Set the Current tmp Working Directory inside the container
WORKDIR /tmp/go-app

# We want to populate the module cache based on the go.{mod,sum} files.
COPY go.mod .
COPY go.sum .

RUN go mod download

COPY . .

# Build our app
RUN go build -o ./out/go-app .

# Start fresh from a smaller image
FROM alpine:3.9 
RUN apk add ca-certificates

COPY --from=build_base /tmp/go-app/out/go-app /app/go-app

# This container exposes port 8081 to the outside world
EXPOSE 8081

# Run the binary program produced by `go install`
CMD ["/app/go-app"]



