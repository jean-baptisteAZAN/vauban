FROM node:{{.Version}}-slim AS build
WORKDIR /app
{{- if or (eq .PM "pnpm") (eq .PM "yarn")}}
RUN corepack enable
{{- end}}
{{- if eq .PM "bun"}}
RUN npm install -g bun
{{- end}}
COPY package.json {{.Lock}}{{if .Berry}} .yarnrc.yml{{end}} ./
RUN {{if eq .PM "pnpm"}}pnpm install --frozen-lockfile{{else if eq .PM "yarn"}}{{if .Berry}}yarn install --immutable{{else}}yarn install --frozen-lockfile{{end}}{{else if eq .PM "bun"}}bun install --frozen-lockfile{{else}}npm install{{end}}
COPY . .
RUN {{if eq .PM "pnpm"}}pnpm prune --prod{{else if eq .PM "yarn"}}{{if .Berry}}yarn workspaces focus --all --production{{else}}yarn install --production --frozen-lockfile{{end}}{{else if eq .PM "bun"}}bun install --production{{else}}npm prune --omit=dev{{end}}

FROM node:{{.Version}}-slim
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build --chown=node:node /app /app
USER node
EXPOSE {{.Port}}
CMD ["npm", "start"]
