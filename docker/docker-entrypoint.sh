#!/bin/sh
set -e

# Legacy entrypoint for Next.js standalone mode (PostgreSQL)
# For single binary mode, use: crewship start

if [ -n "$DATABASE_URL" ] && echo "$DATABASE_URL" | grep -q "^postgresql://"; then
  echo "Running Prisma migrations (PostgreSQL mode)..."
  npx prisma migrate deploy --schema ./prisma/schema.prisma
fi

echo "Starting Next.js server..."
exec node server.js
