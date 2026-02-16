#!/bin/sh
set -e

echo "Running Prisma migrations..."
npx prisma migrate deploy --schema ./prisma/schema.prisma

echo "Starting Next.js server..."
exec node server.js
