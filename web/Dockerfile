# Multi-stage: build Next.js, then run the standalone server.
FROM node:20-alpine AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
# NEXT_PUBLIC_API_BASE is inlined at build time; auth is via login + HttpOnly
# cookie (no JWT is baked in).
ARG NEXT_PUBLIC_API_BASE=http://localhost:8080
ENV NEXT_PUBLIC_API_BASE=$NEXT_PUBLIC_API_BASE
RUN npm run build

FROM node:20-alpine
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
# Public static assets (fallback copy of the eval summary served at /evals/...).
COPY --from=builder /app/public ./public
EXPOSE 3000
CMD ["node", "server.js"]
