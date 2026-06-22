TOPDIR=$(PWD)
WHOAMI=$(shell whoami)
PLATFORMS ?= linux/amd64,linux/arm64

build:
	go build -o bin/booty cmd/main.go

run:
	cd web && npm run build && cd ..
	go run cmd/main.go --dataDir=data/

image:
	docker build -t ${WHOAMI}/booty .

image-push: image
	docker push ${WHOAMI}/booty

image-run: image
	docker run -ti --rm -v ${TOPDIR}/data:/data -p 8080:8080 -p 69:69/udp ${WHOAMI}/booty --debug=true --dataDir=/data

docker-buildx:
	docker buildx build --platform $(PLATFORMS) -t ${WHOAMI}/booty .

