#!/usr/bin/env bash
set -euo pipefail

SRC=/opt/ubolite
CRX=/opt/ubolite.crx
KEY=/opt/ubolite.pem
UPDATE=/opt/update.xml
POLICY_DIR=/etc/chromium/policies/managed

chromium --pack-extension="${SRC}" --no-sandbox >/dev/null 2>&1 || true
test -f "${CRX}"

ID=$(openssl rsa -in "${KEY}" -pubout -outform DER 2>/dev/null \
  | openssl dgst -sha256 -binary \
  | head -c 16 \
  | od -An -tx1 \
  | tr -d ' \n' \
  | tr '0-9a-f' 'a-p')

VERSION=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([0-9][0-9.]*\)".*/\1/p' "${SRC}/manifest.json" | head -1)
: "${VERSION:=1.0}"

mkdir -p "${POLICY_DIR}"

cat > "${UPDATE}" <<XML
<?xml version="1.0" encoding="UTF-8"?>
<gupdate xmlns="http://www.google.com/update2/response" protocol="2.0">
  <app appid="${ID}">
    <updatecheck codebase="file://${CRX}" version="${VERSION}" />
  </app>
</gupdate>
XML

cat > "${POLICY_DIR}/ublock.json" <<JSON
{
  "ExtensionSettings": {
    "${ID}": {
      "installation_mode": "force_installed",
      "update_url": "file://${UPDATE}"
    }
  }
}
JSON

chmod 644 "${CRX}" "${UPDATE}" "${POLICY_DIR}/ublock.json"
rm -f "${KEY}"

echo "ublock id=${ID} version=${VERSION}"
