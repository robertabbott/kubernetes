package main

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

const (
	HAP_SERVERS = `
    server server_backend0 backend0:0
    server server_backend1 backend1:1`
	HAP_BACKENDS = `
backend bk_ssl_application_backend0
    mode tcp
    balance roundrobin

    # maximum SSL session ID length is 32 bytes.
    stick-table type binary len 32 size 30k expire 30m
   
    acl clienthello req_ssl_hello_type 1
    acl serverhello rep_ssl_hello_type 2
   
    # use tcp content accepts to detects ssl client and server hello.
    tcp-request inspect-delay 5s
    tcp-request content accept if clienthello
   
    # no timeout on response inspect delay by default.
    tcp-response content accept if serverhello
   
    stick on payload_lv(43,1) if clienthello
   
    # Learn on response if server hello.
    stick store-response payload_lv(43,1) if serverhello
   
    option ssl-hello-chk
    server server127.0.0.17000 127.0.0.1:7000

`
	HAP_BACKENDS2 = `
backend bk_ssl_application_backend1
    mode tcp
    balance roundrobin

    # maximum SSL session ID length is 32 bytes.
    stick-table type binary len 32 size 30k expire 30m
   
    acl clienthello req_ssl_hello_type 1
    acl serverhello rep_ssl_hello_type 2
   
    # use tcp content accepts to detects ssl client and server hello.
    tcp-request inspect-delay 5s
    tcp-request content accept if clienthello
   
    # no timeout on response inspect delay by default.
    tcp-response content accept if serverhello
   
    stick on payload_lv(43,1) if clienthello
   
    # Learn on response if server hello.
    stick store-response payload_lv(43,1) if serverhello
   
    option ssl-hello-chk
    server server127.0.0.17001 127.0.0.1:7001

`
	HAP_FE = `
frontend sni
    bind *:443
    mode tcp
    option tcplog
    option dontlognull
	option httpchk
    log global

    capture request header Host len 20

    tcp-request inspect-delay 5s
    tcp-request content accept if { req_ssl_hello_type 1 }

    acl application_backend1 req_ssl_sni -m beg blb.
    acl application_backend0 req_ssl_sni -m beg blb.

    use_backend bk_ssl_application_backend1 if application_backend1
    use_backend bk_ssl_application_backend0 if application_backend0

`
	HAP_FE2 = `
frontend sni
    bind *:443
    mode tcp
    option tcplog
    option dontlognull
	option httpchk
    log global

    capture request header Host len 20

    tcp-request inspect-delay 5s
    tcp-request content accept if { req_ssl_hello_type 1 }

    acl application_backend0 req_ssl_sni -m beg blb.
    acl application_backend1 req_ssl_sni -m beg blb.

    use_backend bk_ssl_application_backend0 if application_backend0
    use_backend bk_ssl_application_backend1 if application_backend1

`
	TEST_CONF = `
global
    daemon
    pidfile /haproxy.pid
    log 127.0.0.1 local0 info

defaults
    timeout connect 5000
    timeout client  50000
    timeout server  50000
`
)

func TestHAProxy(t *testing.T) {}

func TestHAPWriteServersNoServers(t *testing.T) {
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

func TestHAPWriteServers(t *testing.T) {
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
	if s != HAP_SERVERS {
		t.Fatal(s)
		t.Fatal("writeServers wrote an incorrect result when passed two servers")
	}
}

func TestHAPWriteBackends(t *testing.T) {
	b := make([]byte, 2048)
	var buf bytes.Buffer
	err := writeBackends(newBackends(1), &buf, HAP_BACKEND_TEMPLATE)
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
	if s != HAP_BACKENDS {
		t.Fatal("writeBackends wrote an incorrect result")
	}
}

func TestHAPWriteFrontend(t *testing.T) {
	b := make([]byte, 2048)
	var buf bytes.Buffer
	err := writeFrontends(newBackends(2), &buf, HAP_FRONTEND_TEMPLATE)
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
	if s != HAP_FE && s != HAP_FE2 {
		t.Fatal("writeFrontends wrote an incorrect result")
	}
}

func TestHAPWriteConfigToFile(t *testing.T) {
	backends := newBackends(2)
	servers := newLBServer(2)
	base := NewBaseCfg(HAPROXY_PID_FILE, "127.0.0.1", 4, 256)
	b := make([]byte, 4096)
	var buf bytes.Buffer
	err := writeHAPConfigToFile(backends, servers, base, &buf, HAP_BASE_CFG_TEMPLATE, HAP_FRONTEND_TEMPLATE, HAP_BACKEND_TEMPLATE, HAP_SERVER_TEMPLATE, HAP_END_TEMPLATE)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buf.Read(b)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	s := trimZeros(b)
	test_conf := fmt.Sprintf("%s%s%s%s", TEST_CONF, HAP_FE, HAP_BACKENDS, HAP_BACKENDS2)
	test_conf2 := fmt.Sprintf("%s%s%s%s", TEST_CONF, HAP_FE2, HAP_BACKENDS, HAP_BACKENDS2)
	test_conf3 := fmt.Sprintf("%s%s%s%s", TEST_CONF, HAP_FE, HAP_BACKENDS2, HAP_BACKENDS)
	test_conf4 := fmt.Sprintf("%s%s%s%s", TEST_CONF, HAP_FE2, HAP_BACKENDS2, HAP_BACKENDS)
	if s != test_conf && s != test_conf2 && s != test_conf3 && s != test_conf4 {
		t.Fatal(test_conf, s)
		t.Fatal("WriteConfigToFile wrote an incorrect result")
	}
}

// creates list of LBServers with different ports
// and different backends
func newLBServer(count int) []LBServer {
	res := []LBServer{}
	for i := 0; i < count; i++ {
		res = append(res, LBServer{
			Port:        i,
			BackendName: fmt.Sprintf("backend%d", i),
		})
	}
	return res
}

// creates list of servers with different ports
func newServers(count int) []*Server {
	res := []*Server{}
	res = append(res, &Server{
		Host: "127.0.0.1",
		Port: 7000 + count,
	})
	return res
}

// checks if slice is empty (== slice zero value)
func empty(b []byte) bool {
	t := make([]byte, len(b))
	return bytes.Equal(t, b)
}

// removes trailing zeroes from a slice
func trimZeros(b []byte) string {
	return string(bytes.TrimRight(b, string([]byte{0})))
}

// creates list of backends with unique names
func newBackends(count int) map[string]Backend {
	res := make(map[string]Backend)
	for i := 0; i < count; i++ {
		res[fmt.Sprintf("backend%d", i)] = Backend{
			Name:    fmt.Sprintf("backend%d", i),
			SNI:     "blb.",
			Servers: newServers(i),
		}
	}
	return res
}
