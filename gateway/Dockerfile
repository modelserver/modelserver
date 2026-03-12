# Stage 1: Build dashboard
FROM node:22-alpine AS dashboard-build
RUN corepack enable pnpm
WORKDIR /app
COPY dashboard/package.json dashboard/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY dashboard/ .
ARG VITE_API_BASE_URL
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}
RUN pnpm build

# Stage 2: Nginx gateway
FROM nginx:alpine
COPY gateway/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=dashboard-build /app/dist /usr/share/nginx/html/dashboard
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
