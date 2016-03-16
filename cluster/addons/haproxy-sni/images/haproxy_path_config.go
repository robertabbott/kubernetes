package main

import (
	"os"
)

const HAP_BASE_CFG_TEMPLATE_PBR = `
global
    daemon
    pidfile {{.PidFile}}
    log {{.SyslogAddr}} local0 debug

defaults
    timeout connect 5000
    timeout client  50000
    timeout server  50000
`

// HAProxy listens on port 8888 for unencrypted
// http traffic and routes based on path
const HAP_FRONTEND_PATH_TEMPLATE = `
frontend pbr
    mode http
    bind *:8888
    option dontlognull
    option httpchk
    option httplog
    log global
    default_backend path_based_app_default_service

{{range .BackendList}}    acl application_{{.Name}} path_sub api/v1/{{.SNI}}
{{end}}
{{range .BackendList}}    use_backend path_based_app_{{.Name}} if application_{{.Name}}
{{end}}`

const HAP_BACKEND_PATH_TEMPLATE = `
backend path_based_app_{{.Name}}
    mode http
    balance roundrobin

{{range .Servers}}    server server{{.Host}}{{.Port}} {{.}}
{{end}}
`

// uses path-based routing instead of sni
type HAProxyPBLB struct {
	Base BaseCfg

	Backends  map[string]Backend
	LBServers []LBServer

	// path to config file
	// ex: /etc/brkt/blb.cfg
	ConfigFilePath string
}

// LBConfig constructor
func NewPBLBConfigWriter(cfgFile, syslogAddr string, backends map[string]Backend, servers []LBServer) LBConfigWriter {
	return &HAProxyPBLB{
		Base:           NewBaseCfg(HAPROXY_PID_FILE, syslogAddr, 4, 256),
		Backends:       backends,
		LBServers:      servers,
		ConfigFilePath: cfgFile,
	}
}

func (h *HAProxyPBLB) GetBackends() map[string]Backend {
	return h.Backends
}

func (h *HAProxyPBLB) SetBackends(backends map[string]Backend) {
	h.Backends = backends
}

func (h *HAProxyPBLB) SetSyslogAddr(addr string) {
	h.Base.SyslogAddr = addr
}

func (h *HAProxyPBLB) ClearLbServers() {
	h.LBServers = []LBServer{}
}

func (h *HAProxyPBLB) AddLbServer(lbs LBServer) {
	h.LBServers = append(h.LBServers, lbs)
}

func (h *HAProxyPBLB) SetBackend(path string, backend Backend) {
	h.Backends[path] = backend
}

func (h *HAProxyPBLB) RemoveBackend(path string) {
	delete(h.Backends, path)
}

func (h *HAProxyPBLB) GetConfigFilePath() string {
	return h.ConfigFilePath
}

func (h *HAProxyPBLB) WriteConfigFile() error {
	// Use create in case file doesn't already exist.
	// If file exists create will truncate it which is fine.
	// file implements io.Writer so the 'file' handle can be
	// passed to the write* methods
	file, err := os.Create(h.ConfigFilePath)
	if err != nil {
		return err
	}
	err = writeHAPConfigToFile(h.Backends, h.LBServers, h.Base, file, HAP_BASE_CFG_TEMPLATE_PBR, HAP_FRONTEND_PATH_TEMPLATE, HAP_BACKEND_PATH_TEMPLATE, HAP_SERVER_TEMPLATE, HAP_END_TEMPLATE)
	if err != nil {
		return err
	}
	return file.Close()
}
