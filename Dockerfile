FROM golang:1.19-bullseye AS build

WORKDIR /src
COPY . /src
RUN go build -o /src/main ./cmd/main.go && chmod +x ./main

FROM gcr.io/distroless/base-debian11:latest
WORKDIR /action
COPY --from=build /src/main /action/action
ENTRYPOINT [ "/action/action" ]
