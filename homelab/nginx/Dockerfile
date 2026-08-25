# 프론트(Vue SPA) 빌드 + nginx 라우팅/서빙을 한 이미지로 — AWS의 S3+CloudFront
# 역할을 하나로 합친 것. 빌드 컨텍스트는 리포 루트(frontend/)가 필요해서
# docker-compose.yml에서 context를 ../ (homelab의 부모, 즉 리포 루트)로 잡는다.
FROM node:22-alpine AS build
WORKDIR /src
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM nginx:1.27-alpine
COPY homelab/nginx/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 80
