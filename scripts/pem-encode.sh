#!/bin/sh
# Encode a PEM file into a single-line string suitable for environment variables.
# Usage: ./pem-encode.sh path/to/key.pem
# Output: one line of base64 without headers, footers, or newlines.
if [ -z "$1" ]; then
  echo "usage: $0 <pem-file>" >&2
  exit 1
fi
grep -v '^-----' "$1" | tr -d '\n'
echo
