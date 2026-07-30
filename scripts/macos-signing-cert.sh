#!/usr/bin/env bash
#
# Create a self-signed code signing certificate for FileSync's macOS builds.
#
# Why: an unsigned (or ad-hoc signed) .app has no stable identity, so macOS
# TCC (Documents / Downloads / Desktop access) and Keychain ACLs have nothing
# durable to attach an approval to, and re-prompt users after rebuilds and
# sometimes after plain relaunches. A self-signed certificate gives the
# bundle a fixed designated requirement, so approvals stick across launches
# and across version updates.
#
# This does NOT satisfy Gatekeeper or notarization — first launch still shows
# the "unidentified developer" dialog. That part needs a paid Developer ID.
#
# End users do not need this certificate installed or trusted: the designated
# requirement pins the leaf certificate by hash, with no chain validation.
#
# Usage:
#   scripts/macos-signing-cert.sh [common-name]
#
# Produces ~/.filesync-signing/ (gitignored territory, outside the repo) and
# prints the two values to store as GitHub Actions secrets.

set -euo pipefail

CN="${1:-FileSync Self-Signed}"
OUT="$HOME/.filesync-signing"
LOGIN_KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"

# Always the system LibreSSL, never whatever conda/homebrew put on PATH.
# OpenSSL 3 writes PKCS#12 with PBES2/AES and a SHA-256 MAC, which macOS's
# Security framework cannot read — `security import` fails with the misleading
# "MAC verification failed ... (wrong password?)".
OPENSSL=/usr/bin/openssl

if [ "$(uname -s)" != "Darwin" ]; then
  echo "This script only runs on macOS." >&2
  exit 1
fi

mkdir -p "$OUT"
chmod 700 "$OUT"

if security find-certificate -c "$CN" >/dev/null 2>&1; then
  echo "A certificate named '$CN' already exists in your keychain."
  echo "Delete it first (Keychain Access -> login -> My Certificates) or pass a different name."
  exit 1
fi

P12_PASSWORD="$(openssl rand -base64 24)"

cat > "$OUT/openssl.cnf" <<EOF
[ req ]
distinguished_name = dn
x509_extensions    = ext
prompt             = no

[ dn ]
CN = $CN

[ ext ]
basicConstraints     = critical,CA:false
keyUsage             = critical,digitalSignature
extendedKeyUsage     = critical,codeSigning
subjectKeyIdentifier = hash
EOF

echo "==> Generating key and self-signed certificate (10 year validity)"
"$OPENSSL" req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -keyout "$OUT/key.pem" -out "$OUT/cert.pem" -config "$OUT/openssl.cnf" 2>/dev/null

"$OPENSSL" pkcs12 -export \
  -inkey "$OUT/key.pem" -in "$OUT/cert.pem" \
  -out "$OUT/cert.p12" -name "$CN" \
  -certpbe PBE-SHA1-3DES -keypbe PBE-SHA1-3DES -macalg sha1 \
  -passout pass:"$P12_PASSWORD"

chmod 600 "$OUT"/*.pem "$OUT"/*.p12

echo "==> Importing into your login keychain"
security import "$OUT/cert.p12" -k "$LOGIN_KEYCHAIN" -P "$P12_PASSWORD" \
  -T /usr/bin/codesign -T /usr/bin/security

echo
echo "==> Trusting the certificate for code signing."
echo "    macOS will ask for your login password (this is the step that needs you)."
security add-trusted-cert -r trustRoot -p codeSign -k "$LOGIN_KEYCHAIN" "$OUT/cert.pem"

echo
echo "==> Allowing codesign to use the key without prompting on every build."
echo "    Enter your login keychain password when asked."
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "" "$LOGIN_KEYCHAIN" >/dev/null 2>&1 || \
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s "$LOGIN_KEYCHAIN" >/dev/null

echo
echo "==> Verifying by actually signing something"
# find-identity -v is not a reliable check here (it reports 0 valid identities
# for a self-signed leaf in some trust configurations), so sign a scratch
# binary and confirm a designated requirement comes out the other side.
PROBE="$(mktemp -d)/probe"
cp /bin/echo "$PROBE"
if ! codesign --force --sign "$CN" "$PROBE" 2>&1; then
  echo "codesign failed with identity '$CN'. Check Keychain Access." >&2
  exit 1
fi
if ! codesign -d -r- "$PROBE" 2>&1 | grep -q "designated"; then
  echo "Signed, but no designated requirement was produced." >&2
  exit 1
fi
echo "Designated requirement:"
codesign -d -r- "$PROBE" 2>&1 | grep designated
rm -rf "$(dirname "$PROBE")"

echo
echo "======================================================================"
echo "Done. Local builds: export APPLE_SIGNING_IDENTITY=\"$CN\""
echo
echo "GitHub Actions secrets to create:"
echo
echo "  MACOS_SIGNING_IDENTITY      = $CN"
echo "  MACOS_CERTIFICATE_PASSWORD  = $P12_PASSWORD"
echo "  MACOS_CERTIFICATE           = (base64 below, one line)"
echo
base64 < "$OUT/cert.p12" | tr -d '\n'
echo
echo
echo "Keep $OUT backed up somewhere safe. If you lose the key and have to"
echo "issue a new certificate, every user's saved permissions reset once."
echo "======================================================================"
