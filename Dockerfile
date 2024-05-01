FROM golang:1.22 AS builder

ARG GOFLAGS=""
ENV GOFLAGS=$GOFLAGS

# Move to working directory /build
WORKDIR /build

# Copy and download dependency using go mod
COPY go.mod .
COPY go.sum .
RUN go mod download

# Copy the code into the container
COPY . .

# Build the application
RUN go build ./cmd/fe

# Build release image
FROM alpine:3.19.1

RUN apk add libc6-compat

COPY --from=builder /build/fe /bin/fe
ENTRYPOINT ["/bin/fe"]
