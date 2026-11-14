# syntax=docker/dockerfile:1

FROM node:26-alpine AS web
RUN npm install -g pnpm@11.7.0
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/gempir/agenthold/internal/version.Version=${VERSION} -X github.com/gempir/agenthold/internal/version.Commit=${COMMIT}" \
      -o /out/agenthold ./cmd/agenthold \
 && CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/gempir/agenthold/internal/version.Version=${VERSION} -X github.com/gempir/agenthold/internal/version.Commit=${COMMIT}" \
      -o /out/sandboxd ./cmd/sandboxd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agenthold /usr/local/bin/agenthold
COPY --from=build /out/sandboxd /usr/local/lib/agenthold/sandboxd
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/agenthold"]
