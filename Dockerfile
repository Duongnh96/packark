FROM node:24-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
COPY backend/internal/api/dist /app/backend/internal/api/dist
RUN npm run build

FROM golang:1.25-alpine AS backend
WORKDIR /src
COPY backend/ ./
COPY --from=frontend /app/backend/internal/api/dist ./internal/api/dist
RUN CGO_ENABLED=0 go build -o /packark ./cmd/packark

FROM alpine:3.22
# docker CLI only - the daemon comes from the host socket mounted at runtime.
RUN apk add --no-cache docker-cli
COPY --from=backend /packark /usr/local/bin/packark
EXPOSE 8080
CMD ["packark"]
