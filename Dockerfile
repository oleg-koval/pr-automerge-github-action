FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pr-automerge-action ./cmd/pr-automerge-action

FROM alpine:3.20
RUN adduser -D -u 10001 action
COPY --from=build /out/pr-automerge-action /usr/local/bin/pr-automerge-action
USER action
ENTRYPOINT ["/usr/local/bin/pr-automerge-action"]
