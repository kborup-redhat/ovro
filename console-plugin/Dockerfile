FROM node:22 AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM registry.access.redhat.com/ubi9/ubi-minimal:9.6
RUN microdnf install -y nginx && microdnf clean all && \
    chmod -R g+rwx /var/log/nginx /run && \
    chown -R 1001:0 /var/log/nginx /run
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/nginx.conf
USER 1001
EXPOSE 9443
CMD ["nginx", "-e", "/dev/stderr", "-g", "daemon off;"]
