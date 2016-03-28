.PHONY:	haproxy_sni build push

# The current value of the tag to be used for building and
# pushing an image to gcr.io
TAG = 1.3
PBR_TAG = 1.1

haproxy_sni:
	godep go test .
	go build -o haproxy-monitor .

build:	haproxy_sni
	#docker build -t gcr.io/google_containers/haproxy_sni:$(TAG) .
	docker build -t registry.int.brkt.net/brkt/haproxy-sni:v$(TAG) .

push: build
	docker push registry.int.brkt.net/brkt/haproxy-sni
	#gcloud docker push gcr.io/google_containers/haproxy_sni:$(TAG)

build-pbr: haproxy_sni
	docker build -t registry.int.brkt.net/brkt/haproxy-pbr:v$(PBR_TAG) .

push-pbr: build-pbr
	docker push registry.int.brkt.net/brkt/haproxy-pbr

clean:
	rm haproxy-monitor
