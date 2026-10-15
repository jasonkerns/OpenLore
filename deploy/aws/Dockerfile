# Agent Harness build of OpenLore for AWS (ECS Fargate behind a load balancer).
# Build from the repository root:  docker build -f deploy/aws/Dockerfile .
# CodeBuild does this with deploy/aws/buildspec.yml.

FROM public.ecr.aws/docker/library/node:24-alpine AS dashboard
WORKDIR /src
COPY dashboard/package.json dashboard/package-lock.json dashboard/
RUN npm --prefix dashboard ci
COPY dashboard dashboard
COPY assets/dashboard assets/dashboard
RUN npm --prefix dashboard run build

FROM public.ecr.aws/docker/library/golang:1.26-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=dashboard /src/assets/dashboard/dist assets/dashboard/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /out/openlore ./cmd/openlore

FROM public.ecr.aws/docker/library/alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 openlore
COPY --from=server /out/openlore /usr/local/bin/openlore
COPY --chmod=755 deploy/aws/start.sh /usr/local/bin/openlore-start
USER openlore
# /data must be persistent (an EFS volume): config, signing key, passkeys, history and the docs live there.
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/openlore-start"]
