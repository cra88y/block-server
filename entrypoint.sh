#!/bin/sh
set -e

# Build SSL parameters if certs exist
SSL_OPTS=""
if [ -f "/certs/ca.crt" ]; then
  SSL_OPTS="sslmode=verify-full&sslrootcert=/certs/ca.crt"
fi

DB_ADDRESS="${DB_USER?DB_USER not set}:${DB_PASS?DB_PASS not set}@cockroachdb:${DB_PORT?DB_PORT not set}/${DB_NAME?DB_NAME not set}${SSL_OPTS:+?$SSL_OPTS}"

echo "Running database migrations..."
/nakama/nakama migrate up --database.address "${DB_ADDRESS}"

echo "Starting nakama..."
exec /nakama/nakama \
  --config /nakama/data/nakama-config.yml \
  --database.address "${DB_ADDRESS}" \
  --socket.server_key "${SOCKET_SERVER_KEY}" \
  --runtime.http_key "${RUNTIME_HTTP_KEY}" \
  --session.encryption_key "${SESSION_ENCRYPTION_KEY}" \
  --session.refresh_encryption_key "${SESSION_REFRESH_ENCRYPTION_KEY}" \
  --console.username "${CONSOLE_USERNAME}" \
  --console.password "${CONSOLE_PASSWORD}" \
  --console.signing_key "${CONSOLE_SIGNING_KEY}" \
  --logger.level "${NAKAMA_LOG_LEVEL}"
