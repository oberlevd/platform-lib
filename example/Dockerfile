FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/example
RUN go mod tidy && CGO_ENABLED=0 go build -o /out/example .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/example /example
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/example"]
