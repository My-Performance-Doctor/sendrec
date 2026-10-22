# MPD's own build of Dockerfile.base in the staging account's ECR, pinned by
# digest. CI builds Dockerfile.base and passes its local tag here instead.
ARG BASE_IMAGE=537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec-base@sha256:1873a2db2afa5a76aa30699c149ed6675480fd7724fae2b18cd694b5eb105a20

# Stage 1: Build frontend
FROM node:24-alpine AS frontend
ENV PNPM_CONFIG_STRICT_DEP_BUILDS=false
WORKDIR /app/web
COPY web/package.json web/pnpm-lock.yaml* ./
RUN corepack enable && pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm build

# Stage 2: Build Go binary
FROM golang:1.26.10-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o sendrec ./cmd/sendrec

# Stage 3: Final image (base includes whisper-cli, ffmpeg, RNNoise model)
FROM ${BASE_IMAGE}
# Check the CA store this image ships, whatever built the base.
RUN --mount=type=bind,source=scripts/check-ca-roots.sh,target=/tmp/check-ca-roots.sh sh /tmp/check-ca-roots.sh
COPY --from=backend /app/sendrec .
# --chmod: a checkout made under a strict umask (077, or 027 on hardened
# hosts) leaves the script unreadable to the sendrec user, and the container
# restart-loops on "Permission denied".
COPY --chmod=755 docker-entrypoint.sh .
USER sendrec
EXPOSE 8080
ENTRYPOINT ["./docker-entrypoint.sh"]
CMD ["./sendrec"]
