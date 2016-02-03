package main

import (
	"bytes"
	"io"
	"testing"
)

const (
	HAPPB_SERVERS = `
    server server_backend0 backend0:0
    server server_backend1 backend1:1`
	HAPPB_BACKENDS = `
backend path_based_app_backend0
    mode http
    balance roundrobin

    server server127.0.0.17000 127.0.0.1:7000

`
	HAPPB_FE = `
frontend pbr
    mode http
    bind *:8888
    option dontlognull
    option httpchk
    option httplog
    log global
    default_backend path_based_app_old_yeti_api

    acl application_backend1 path_sub api/v1/blb.
    acl application_backend0 path_sub api/v1/blb.

    use_backend path_based_app_backend1 if application_backend1
    use_backend path_based_app_backend0 if application_backend0
`
	HAPPB_FE2 = `
frontend pbr
    mode http
    bind *:8888
    option dontlognull
    option httpchk
    option httplog
    log global
    default_backend path_based_app_old_yeti_api

    acl application_backend0 path_sub api/v1/blb.
    acl application_backend1 path_sub api/v1/blb.

    use_backend path_based_app_backend0 if application_backend0
    use_backend path_based_app_backend1 if application_backend1
`
)

func TestHAPPBWriteServersNoServers(t *testing.T) {
	b := make([]byte, 2048)
	var buf bytes.Buffer
	servers := newLBServer(0)
	err := writeServers(servers, make(map[string]Backend), &buf, HAP_SERVER_TEMPLATE)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buf.Read(b)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	s := trimZeros(b)
	if s != "" {
		t.Fatal("writeServers wrote an incorrect result when passed 0 servers")
	}
}

func TestHAPPBWriteServers(t *testing.T) {
	b := make([]byte, 2048)
	var buf bytes.Buffer
	backends := newBackends(2)
	servers := newLBServer(2)
	err := writeServers(servers, backends, &buf, HAP_SERVER_TEMPLATE)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buf.Read(b)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	s := trimZeros(b)
	if s != HAPPB_SERVERS {
		t.Fatal(s)
		t.Fatal("writeServers wrote an incorrect result when passed two servers")
	}
}

func TestHAPPBWriteBackends(t *testing.T) {
	b := make([]byte, 2048)
	var buf bytes.Buffer
	err := writeBackends(newBackends(1), &buf, HAP_BACKEND_PATH_TEMPLATE)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buf.Read(b)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if empty(b) {
		t.Fatal("writeBackends returned empty string even though a backend was provided")
	}
	s := trimZeros(b)
	if s != HAPPB_BACKENDS {
		t.Fatal("writeBackends wrote an incorrect result")
	}
}

func TestHAPPBWriteFrontend(t *testing.T) {
	b := make([]byte, 2048)
	var buf bytes.Buffer
	err := writeFrontends(newBackends(2), &buf, HAP_FRONTEND_PATH_TEMPLATE)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buf.Read(b)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if empty(b) {
		t.Fatal("writeFrontends returned empty string even though a backend was provided")
	}
	s := trimZeros(b)
	if s != HAPPB_FE && s != HAPPB_FE2 {
		t.Fatal(s)
		t.Fatal("writeFrontends wrote an incorrect result")
	}
}
