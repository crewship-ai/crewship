FROM node:24-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends python3 ansible-core default-mysql-client && rm -rf /var/lib/apt/lists/*
USER 1000:1000
WORKDIR /work
