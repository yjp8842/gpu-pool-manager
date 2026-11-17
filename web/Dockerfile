# 화면 이미지. web/를 컨텍스트로 빌드한다.
#   docker build -t gpu-pool/web:dev web
# nginx가 정적 파일을 내주고, /api와 /healthz는 서버2(API_UPSTREAM)로 넘긴다. 개발 서버의 vite proxy와 같은 규칙이다.
FROM node:26-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.28-alpine
# nginx 공식 이미지는 시작할 때 /etc/nginx/templates/*.template의 ${변수}를 환경 변수로 채워 /etc/nginx/conf.d/에 둔다.
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html
ENV API_UPSTREAM=api:8080 NGINX_ENVSUBST_FILTER=API_UPSTREAM
EXPOSE 8080
