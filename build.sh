#!/bin/sh
set -eu
web_output=dist
if [ "${1-}" = --check ]; then
  web_output=$(mktemp -d)
  trap 'rm -rf "$web_output"' EXIT
fi
npm --prefix web ci --no-audit --no-fund
npm --prefix web run build -- --outDir "$web_output"
if [ "${1-}" = --check ]; then
  diff -r web/dist "$web_output"
else
  go build -o inode ./cmd/inode
fi
