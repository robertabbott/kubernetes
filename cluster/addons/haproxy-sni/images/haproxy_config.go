package main

import (
	"fmt"
	"html/template"
	"io"
	"os"
)

// templates for each section of the haproxy config
const HAPROXY_PID_FILE = "/haproxy.pid"
const HAPROXY_CONFIG_PATH = "/haproxy.cfg"
const HAP_BASE_CFG_TEMPLATE = `
global
    daemon
    pidfile {{.PidFile}}
    log {{.SyslogAddr}} local0 info

defaults
    timeout connect 5000
    timeout client  50000
    timeout server  50000
`

// HAProxy listens on port 443
const HAP_FRONTEND_TEMPLATE = `
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

{{range .BackendList}}    acl application_{{.Name}} req_ssl_sni -m beg {{.SNI}}
{{end}}
{{range .BackendList}}    use_backend bk_ssl_application_{{.Name}} if application_{{.Name}}
{{end}}
`

const HAP_BACKEND_TEMPLATE = `
backend bk_ssl_application_{{.Name}}
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
   
    option tcp-check
    option log-health-checks
    default-server inter 10s fall 2 rise 2
{{range .Servers}}    server server{{.Host}}{{.Port}} {{.}} check port {{.Port}}{{if .Proxy}} send-proxy{{end}}
{{end}}
`

const HAP_SERVER_TEMPLATE = `
    server server_{{.BackendName}} {{.BackendName}}:{{.Port}}`

const HAP_END_TEMPLATE = ``

type backends struct {
	BackendList []Backend
}

type LBConfigWriter interface {
	WriteConfigFile() error
	GetBackends() map[string]Backend
	SetBackend(string, Backend)
	SetBackends(backends map[string]Backend)
	RemoveBackend(string)
	SetSyslogAddr(string)
	ClearLbServers()
	AddLbServer(LBServer)
	GetConfigFilePath() string
}

// Represents base level haproxy options
type BaseCfg struct {
	// specifies file that will store haproxy's pid
	PidFile string

	SyslogAddr string

	// number of worker processes haproxy will spawn
	Processes int

	// maximum number of simultaneous connections each worker process can open
	WorkerConns int
}

// Represents a backend. In yeti-world each backend corresponds
// to a set of pods running a service.
type Backend struct {
	// name of backend
	Name string

	// host
	SNI string

	// slice of servers in the backend ex: []string{"1.2.3.4:9000"}
	Servers []*Server
}

type Server struct {
	Host  string
	Port  int
	Proxy bool
}

func (s *Server) String() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

// haproxy Server. This struct represents each haproxy load balancer running
// on the LB instance. Servers listen on port {Port} and load balance traffic
// to servers in the backend with Name == BackendName
type LBServer struct {
	// port this server listens on
	Port int

	// name of backend this server passes traffic to
	// haproxyConfig[LBServer.BackendName] returns
	// the backend this LBServer routes to
	BackendName string
}

func NewBackends(m map[string]Backend) *backends {
	b := []Backend{}
	for _, backend := range m {
		if backend.SNI != "" {
			b = append(b, backend)
		}
	}
	return &backends{
		BackendList: b,
	}
}

type HAProxyLB struct {
	Base BaseCfg

	// maps BackendName to each Backend
	// backend names in haproxy should be
	// the whole url (ie caservice.demo.bobby.brkt.net)
	Backends  map[string]Backend
	LBServers []LBServer

	// path to config file
	// ex: /etc/brkt/blb.cfg
	ConfigFilePath string
}

var _ LBConfigWriter = (*HAProxyLB)(nil)

// LBConfig constructor
func NewLBConfigWriter(cfgFile, syslogAddr string, backends map[string]Backend, servers []LBServer) LBConfigWriter {
	return &HAProxyLB{
		Base:           NewBaseCfg(HAPROXY_PID_FILE, syslogAddr, 4, 256),
		Backends:       backends,
		LBServers:      servers,
		ConfigFilePath: cfgFile,
	}
}

func (h *HAProxyLB) GetBackends() map[string]Backend {
	return h.Backends
}

func (h *HAProxyLB) SetSyslogAddr(addr string) {
	h.Base.SyslogAddr = addr
}

func (h *HAProxyLB) AddLbServer(lbs LBServer) {
	h.LBServers = append(h.LBServers, lbs)
}

func (h *HAProxyLB) ClearLbServers() {
	h.LBServers = []LBServer{}
}

func (h *HAProxyLB) SetBackends(backends map[string]Backend) {
	h.Backends = backends
}

func (h *HAProxyLB) SetBackend(path string, backend Backend) {
	h.Backends[path] = backend
}

func (h *HAProxyLB) RemoveBackend(path string) {
	delete(h.Backends, path)
}

func (h *HAProxyLB) GetConfigFilePath() string {
	return h.ConfigFilePath
}

func (h *HAProxyLB) WriteConfigFile() error {
	// Use create in case file doesn't already exist.
	// If file exists create will truncate it which is fine.
	// file implements io.Writer so the 'file' handle can be
	// passed to the write* methods
	file, err := os.Create(h.ConfigFilePath)
	if err != nil {
		return err
	}
	err = writeHAPConfigToFile(h.Backends, h.LBServers, h.Base, file, HAP_BASE_CFG_TEMPLATE, HAP_FRONTEND_TEMPLATE, HAP_BACKEND_TEMPLATE, HAP_SERVER_TEMPLATE, HAP_END_TEMPLATE)
	if err != nil {
		return err
	}
	return file.Close()
}

// Writes haproxy config file
func writeHAPConfigToFile(backends map[string]Backend, servers []LBServer, base BaseCfg, file io.Writer, baseTpl, feTpl, backendTpl, serverTpl, endTpl string) error {
	err := writeBaseCfg(base, file, baseTpl)
	if err != nil {
		return err
	}
	err = writeFrontends(backends, file, feTpl)
	if err != nil {
		return err
	}
	err = writeBackends(backends, file, backendTpl)
	if err != nil {
		return err
	}
	return writeEndCfg(file, endTpl)
}

func writeFrontends(backends map[string]Backend, writer io.Writer, feTpl string) error {
	tmpl, err := template.New("test").Parse(feTpl)
	if err != nil {
		return err
	}
	return tmpl.Execute(writer, NewBackends(backends))
}

// BaseCfg constructor
func NewBaseCfg(pidfile, syslogAddr string, proc, conns int) BaseCfg {
	return BaseCfg{
		SyslogAddr:  syslogAddr,
		PidFile:     pidfile,
		Processes:   proc,
		WorkerConns: conns,
	}
}

// Writes haproxy config file
func writeConfigToFile(backends map[string]Backend, servers []LBServer, base BaseCfg, file io.Writer, baseTpl, backendTpl, serverTpl, endTpl string) error {
	err := writeBaseCfg(base, file, baseTpl)
	if err != nil {
		return err
	}
	err = writeBackends(backends, file, backendTpl)
	if err != nil {
		return err
	}
	err = writeServers(servers, backends, file, serverTpl)
	if err != nil {
		return err
	}
	return writeEndCfg(file, endTpl)
}

// Writes base config to the writer
func writeBaseCfg(base BaseCfg, writer io.Writer, baseTpl string) error {
	tmpl, err := template.New("test").Parse(baseTpl)
	if err != nil {
		return err
	}
	err = tmpl.Execute(writer, base)
	return err
}

// Writes a list of backends to the writer
func writeBackends(backends map[string]Backend, writer io.Writer, backendTpl string) error {
	tmpl, err := template.New("test").Parse(backendTpl)
	if err != nil {
		return err
	}
	// Execute each backend. Execute write the text output
	// of a template to an io.Writer (os.File implements io.Writer)
	for _, backend := range backends {
		// if there are no servers in the backend
		// don't write this Backend
		if len(backend.Servers) == 0 {
			continue
		}
		err = tmpl.Execute(writer, backend)
		if err != nil {
			return err
		}
	}
	return nil
}

// Writes a list of haproxy servers to the writer
func writeServers(servers []LBServer, backends map[string]Backend, writer io.Writer, serverTpl string) error {
	tmpl, err := template.New("test").Parse(serverTpl)
	if err != nil {
		return err
	}
	for _, s := range servers {
		// if there are no servers in the backend
		// don't write this LBServer
		if len(backends[s.BackendName].Servers) == 0 {
			continue
		}
		err = tmpl.Execute(writer, s)
		if err != nil {
			return err
		}
	}
	return nil
}

// Writes closing curly brace to writer
func writeEndCfg(writer io.Writer, endTpl string) error {
	tmpl, err := template.New("test").Parse(endTpl)
	if err != nil {
		return err
	}
	err = tmpl.Execute(writer, nil)
	return err
}
