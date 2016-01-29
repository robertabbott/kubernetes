package main

import (
	"os"
)

// HAProxy listens on port 8000 for unencrypted
// http traffic and routes based on path
const HAP_FRONTEND_PATH_TEMPLATE = `
frontend http
    bind *:8000
    option dontlognull
    option httpchk
    log global
    default_backend path_based_app_old_yeti_api

{{range .BackendList}}    acl application_{{.Name}} path_reg -i {{.SNI}}*
{{end}}
{{range .BackendList}}    use_backend path_based_app_{{.Name}} if application_{{.Name}}
{{end}}`

const HAP_BACKEND_PATH_TEMPLATE = `
backend path_based_app_{{.Name}}
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
	// add default route
	backends["old_yeti_api"] = Backend{
		Name: "old_yeti_api",
		SNI:  "",
		Servers: []*Server{
			&Server{
				// brkt-specific af
				Host: "yetiapi.internal", // blb.*
				Port: SVC_PORT,           // 443
			},
		},
	}

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
	err = writeHAPConfigToFile(h.Backends, h.LBServers, h.Base, file, HAP_BASE_CFG_TEMPLATE, HAP_FRONTEND_PATH_TEMPLATE, HAP_BACKEND_PATH_TEMPLATE, HAP_SERVER_TEMPLATE, HAP_END_TEMPLATE)
	if err != nil {
		return err
	}
	return file.Close()
}
