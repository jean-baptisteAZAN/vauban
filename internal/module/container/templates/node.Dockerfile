FROM node:{{.Version}}-slim AS build
WORKDIR /app
{{- if or (eq .PM "pnpm") (eq .PM "yarn")}}
RUN corepack enable
{{- end}}
{{- if eq .PM "bun"}}
RUN npm install -g bun
{{- end}}
COPY package.json {{.Lock}} ./
RUN {{if eq .PM "pnpm"}}pnpm install --frozen-lockfile{{else if eq .PM "yarn"}}yarn install --frozen-lockfile{{else if eq .PM "bun"}}bun install{{else}}npm install{{end}}
COPY . .

FROM node:{{.Version}}-slim
WORKDIR /app
{{- if or (eq .PM "pnpm") (eq .PM "yarn")}}
RUN corepack enable
{{- end}}
{{- if eq .PM "bun"}}
RUN npm install -g bun
{{- end}}
ENV NODE_ENV=production
COPY --from=build --chown=node:node /app /app
USER node
EXPOSE {{.Port}}
CMD {{if eq .PM "bun"}}["bun", "run", "start"]{{else}}["{{.PM}}", "start"]{{end}}
