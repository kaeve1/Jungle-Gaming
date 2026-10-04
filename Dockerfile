# syntax=docker/dockerfile:1
# Versão do Go: manter igual à diretiva "go" do go.mod. Imagens fixadas por digest
# para builds reproduzíveis; ao atualizar a tag, atualizar o digest junto.
ARG GO_VERSION=1.27.1

FROM golang:${GO_VERSION}-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/service \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS runtime
COPY --from=build /out/service /service
COPY --from=build /out/migrate /migrate
USER nonroot:nonroot
ENTRYPOINT ["/service"]
