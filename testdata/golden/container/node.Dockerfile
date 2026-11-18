FROM node:26-slim AS build
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm prune --prod

FROM node:26-slim
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build --chown=node:node /app /app
USER node
EXPOSE 8080
CMD ["npm", "start"]
