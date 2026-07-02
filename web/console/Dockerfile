# --- build stage: compile the Vite SPA -------------------------------------
FROM node:20-alpine AS build
WORKDIR /app

# Install deps against the lockfile first for cacheable layers.
COPY package.json package-lock.json* ./
RUN npm ci || npm install

COPY . .

# The gateway base URL is baked at build time (Vite inlines VITE_* into the bundle).
# Override per-environment: docker build --build-arg VITE_GATEWAY_URL=https://api.example.com
ARG VITE_GATEWAY_URL=http://localhost:8080
ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL
RUN npm run build

# --- serve stage: static nginx --------------------------------------------
FROM nginx:1.27-alpine AS serve
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://localhost:8080/healthz || exit 1
CMD ["nginx", "-g", "daemon off;"]
