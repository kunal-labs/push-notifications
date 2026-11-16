FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sideplane-demo ./cmd/sideplane-demo

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/sideplane-demo /sideplane-demo
ENV PORT=8080
EXPOSE 8080

ENTRYPOINT ["/sideplane-demo", "--mode=server"]
