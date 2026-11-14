# Builds Kyro from this source tree into a minimal image.
# Build context: the root of the repository. See README.md next to this file.
#
# The image contains Kyro and CA certificates, nothing else: no shell, no
# iPXE, no wimboot and no Microsoft files. iPXE and wimboot are fetched into
# the data volume with "kyro bootfiles fetch".

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kyro ./cmd/kyro

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/kyro /usr/local/bin/kyro
COPY LICENSE NOTICE /usr/share/doc/kyro/
ENV PATH=/usr/local/bin
VOLUME /var/lib/kyro
ENTRYPOINT ["/usr/local/bin/kyro"]
CMD ["serve"]
