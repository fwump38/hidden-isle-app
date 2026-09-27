#!/bin/sh
# Re-download the vendored front-end assets. Edit the versions, run, then update static/vendor/VERSIONS.
set -eu
BOOTSTRAP=5.3.8 ICONS=1.13.1 HTMX=2.0.11 SSE=2.2.4
S="$(dirname "$0")/../internal/web/static/vendor"
J=https://cdn.jsdelivr.net/npm
mkdir -p "$S/fonts"
curl -sfo "$S/bootstrap.min.css" "$J/bootstrap@$BOOTSTRAP/dist/css/bootstrap.min.css"
curl -sfo "$S/bootstrap.bundle.min.js" "$J/bootstrap@$BOOTSTRAP/dist/js/bootstrap.bundle.min.js"
curl -sfo "$S/bootstrap-icons.min.css" "$J/bootstrap-icons@$ICONS/font/bootstrap-icons.min.css"
curl -sfo "$S/fonts/bootstrap-icons.woff2" "$J/bootstrap-icons@$ICONS/font/fonts/bootstrap-icons.woff2"
curl -sfo "$S/fonts/bootstrap-icons.woff" "$J/bootstrap-icons@$ICONS/font/fonts/bootstrap-icons.woff"
curl -sfo "$S/htmx.min.js" "$J/htmx.org@$HTMX/dist/htmx.min.js"
curl -sfo "$S/htmx-ext-sse.min.js" "$J/htmx-ext-sse@$SSE/dist/sse.min.js"
echo "vendored bootstrap $BOOTSTRAP, icons $ICONS, htmx $HTMX, sse $SSE"
