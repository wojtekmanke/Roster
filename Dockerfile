# syntax=docker/dockerfile:1

# 1. Build the dashboard.
FROM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. Build the server. Migrations, example agents and the price table are
#    embedded in the binary.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /roster ./cmd/roster

# 3. A minimal runtime image: one static binary and the dashboard's files.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /roster /roster
COPY --from=web /web/dist /web
ENV ROSTER_WEB_DIR=/web \
    PORT=8080
EXPOSE 8080
ENTRYPOINT ["/roster"]
CMD ["serve"]
