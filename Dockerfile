FROM golang:tip-alpine3.22 as build

ARG RELEASE

COPY . /app

WORKDIR /app

ENV CGO_ENABLED=0 RELEASE=$RELEASE

RUN ./make.sh build

FROM alpine:3.22

COPY --from=build /app/app /app

EXPOSE 8005
