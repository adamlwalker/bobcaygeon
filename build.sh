#!/bin/bash
if [[ $TRAVIS_OS_NAME == 'osx' ]]; then
  $GOPATH/bin/goveralls -v -service=travis-ci
fi

go build -o bcg-$TRAVIS_OS_NAME cmd/bcg.go