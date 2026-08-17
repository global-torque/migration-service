#!/usr/bin/env bash

PKG_LIST=`go list ./... | grep -v /vendor/`
GO_FILES=`find . -name '*.go' | grep -v _test.go`
COMPANY=webdevelop-pro
SERVICE=migration-service

build() {
  curl https://github.com/webdevelop-pro/migration-service/releases/download/v1.0.2/app-v1.0.2-`uname`-`uname -m`.tar.gz | tar xz > app
  chmod +x app
}

case $1 in

run)
  ./app
  ;;

build)
  build
  ;;

init)
  ./app --init
  ;;

help)
  cat make.sh | grep "^[a-z-]*)"
  ;;

*)
  echo "unknown"
  ;;

esac
