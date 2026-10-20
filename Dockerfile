# Build the Go service and static UI without shipping Node in the runtime image.
FROM node:22-alpine AS ui-build
WORKDIR /src/ui
COPY ui/package*.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

FROM golang:1.24-alpine AS go-build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/routeweft ./cmd/routeweft
RUN CGO_ENABLED=0 go test -c -o /out/tls-check ./internal/transport

FROM scratch AS tls-check
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/tls-check /tls-check
RUN ["/tls-check", "-test.run=^TestSystemTLSRoots$", "-test.v"]

FROM scratch
COPY --from=tls-check /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/routeweft /routeweft
COPY --from=ui-build /src/ui/dist /ui
ENV ROUTEWEFT_UI_DIR=/ui
EXPOSE 21128
ENTRYPOINT ["/routeweft"]
CMD ["serve"]
