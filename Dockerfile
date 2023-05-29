FROM golang:1.20.0-alpine as builder

ENV PRISMA_VERSION="efdf9b1183dddfd4258cd181a72125755215ab7b"
ENV OS="linux-musl"
ENV QUERY_ENGINE_URL="https://binaries.prisma.sh/all_commits/${PRISMA_VERSION}/${OS}/query-engine.gz"
ENV MIGRATION_ENGINE_URL="https://binaries.prisma.sh/all_commits/${PRISMA_VERSION}/${OS}/migration-engine.gz"

# install prisma
WORKDIR /app/prisma
# download query engine
RUN wget -O query-engine.gz $QUERY_ENGINE_URL
RUN gunzip query-engine.gz
RUN chmod +x query-engine
# download migration engine
RUN wget -O migration-engine.gz $MIGRATION_ENGINE_URL
RUN gunzip migration-engine.gz
RUN chmod +x migration-engine

# build app
WORKDIR /app
ADD . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o main .

FROM alpine
WORKDIR /app

RUN apk add bash fuse3 sqlite ca-certificates

COPY --from=builder /app/main /usr/local/bin/wunderbase
COPY --from=builder /app/prisma/migration-engine /usr/local/bin/migration-engine
COPY --from=builder /app/prisma/query-engine /usr/local/bin/query-engine
COPY --from=flyio/litefs:sha-6421a22 /usr/local/bin/litefs /usr/local/bin/litefs

COPY ./schema.prisma .
COPY litefs.yml /etc/litefs.yml

RUN chmod +x /usr/local/bin/migration-engine
RUN chmod +x /usr/local/bin/query-engine
ENV MIGRATION_LOCK_FILE="/app/migration.lock"
ENV QUERY_ENGINE_PATH="/usr/local/bin/query-engine"
ENV MIGRATION_ENGINE_PATH="/usr/local/bin/migration-engine"
ENV PRISMA_SCHEMA_FILE="/app/schema.prisma"
RUN mkdir /app/data
EXPOSE 4466
ENTRYPOINT ["litefs", "mount"]
