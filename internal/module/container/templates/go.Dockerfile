FROM golang:{{.Version}} AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
EXPOSE {{.Port}}
ENTRYPOINT ["/app"]
