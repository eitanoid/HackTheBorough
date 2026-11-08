FROM node:22-alpine

RUN apk add --no-cache libc6-compat \
    && corepack enable

WORKDIR /app

# Install dependencies separately so source edits can reuse this layer.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

COPY . .

ENV NODE_ENV=development \
    PORT=3000

EXPOSE 3000
CMD ["node", "node_modules/next/dist/bin/next", "dev", "--hostname", "0.0.0.0"]
