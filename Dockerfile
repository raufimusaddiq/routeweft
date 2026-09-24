# Build the Go service and static UI without shipping Node in the runtime image.
FROM node:22-alpine AS ui-build
WORKDIR /src/ui
COPY ui/package*.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

FROM golang:1.24-alpine AS go-build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/routeweft ./cmd/routeweft

FROM scratch
COPY --from=go-build /out/routeweft /routeweft
COPY --from=ui-build /src/ui/dist /ui
EXPOSE 21128
ENTRYPOINT ["/routeweft"]
CMD ["serve"]
