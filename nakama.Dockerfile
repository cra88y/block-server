# we need a custom image of nakama that will build and include our modules

# |1| build go modules
# match the version for both heroic images
FROM heroiclabs/nakama-pluginbuilder:3.32.1 AS builder
ENV GO111MODULE on
ENV CGO_ENABLED 1

WORKDIR /_work
COPY ./go .
RUN go build --trimpath --mod=vendor --buildmode=plugin -o ./backend.so

# |2| build ts modules with lightweight builder
FROM node:24-alpine3.21 AS node-builder

WORKDIR /_work
COPY ./ts/package*.json .
RUN npm install

COPY ./ts .
RUN npm run type-check
RUN npm run build

# |3| build final nakama image
# match the version for both heroic images
FROM registry.heroiclabs.com/heroiclabs/nakama:3.32.1

# copy built ts/go modules
COPY --from=builder /_work/backend.so /nakama/data/modules/	
COPY --from=node-builder /_work/build/*.js /nakama/data/modules/

# copy nakama config
COPY ./nakama-config.yml /nakama/data/nakama-config.yml

# nakama entry script
COPY ./entrypoint.sh /nakama/
RUN chmod +x /nakama/entrypoint.sh

# nakama start script
ENTRYPOINT ["/nakama/entrypoint.sh"]

# clear base image CMD
CMD []
