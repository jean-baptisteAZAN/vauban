FROM python:{{.Version}}-slim AS build
WORKDIR /app
{{- if eq .PM "uv"}}
COPY --from=ghcr.io/astral-sh/uv:{{.UV}} /uv /uvx /bin/
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY . .
RUN uv sync --frozen --no-dev
{{- else}}
RUN python -m venv /app/.venv
ENV PATH="/app/.venv/bin:$PATH"
COPY . .
RUN pip install --no-cache-dir {{if .Lock}}-r {{.Lock}}{{else}}.{{end}}
{{- end}}

FROM python:{{.Version}}-slim
WORKDIR /app
RUN useradd --system --create-home --uid 10001 app
COPY --from=build --chown=app:app /app /app
ENV PATH="/app/.venv/bin:$PATH" \
    PYTHONUNBUFFERED=1
USER app
EXPOSE {{.Port}}
CMD ["python", "{{.Entry}}"]
