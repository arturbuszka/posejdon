FROM docker.io/library/golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /posejdon .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /posejdon /posejdon
ENV ADDR=:8080 DEVICES_FILE=/config/devices.json
EXPOSE 8080
ENTRYPOINT ["/posejdon"]
