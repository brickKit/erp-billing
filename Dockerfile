# The billing component alone: a static binary on Alpine (the health check runs through /bin/sh and wget).
FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /erp-billing .

FROM alpine:3.24.2
COPY --from=build /erp-billing /erp-billing
# the listening port in a container; BrickKit injects PORT only for mode: local
ENV PORT=8089
EXPOSE 8089 9089
ENTRYPOINT ["/erp-billing"]
