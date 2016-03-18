package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/kubernetes/pkg/api"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"

	"github.com/golang/glog"
)

const (
	MAX_RETRIES         = 5
	HAPROXY_NAME        = "haproxy-sni-name"
	HAPROXY_EXPOSE      = "haproxy-sni-expose"
	HAPROXY_PATH_NAME   = "haproxy-pbr-name"
	HAPROXY_PATH_EXPOSE = "haproxy-pbr-expose"
)

type loadbalancer struct {
	// if true write path-based routing config
	// if false write sni routing config
	usePBR bool

	// port to route to for backend services
	svcPort int

	// one ConfigWriter per instance. ConfigWriter has
	// separate lbServers for each service. Each
	// LBServer specifies a SNI. Each LBServer routes traffic
	// to a backend based on the SNI. Each backend specifies
	// a pool of servers to which traffic may be sent
	lbConfigWriter LBConfigWriter

	// maps service names to services
	services map[string]*svc

	// channel passed to each monitor. This channels is used to
	// signal monitors to shutdown when this process receives SIGTERM
	shutdownCh chan struct{}
}

func newLb(usePBR bool, svcPort int) *loadbalancer {
	var lbConfig LBConfigWriter
	if usePBR {
		lbConfig = NewPBLBConfigWriter(HAPROXY_CONFIG_PATH, getSyslogAddr(), make(map[string]Backend), []LBServer{})
	} else {
		lbConfig = NewLBConfigWriter(HAPROXY_CONFIG_PATH, getSyslogAddr(), make(map[string]Backend), []LBServer{})
	}
	return &loadbalancer{
		usePBR:         usePBR,
		svcPort:        svcPort,
		lbConfigWriter: lbConfig,
		services:       make(map[string]*svc),
		shutdownCh:     make(chan struct{}),
	}
}

type svc struct {
	// service-specific prefix for this service
	sni string

	// Internal information about containers. ContainerPort
	// and PodIP will be used to construct backends for
	// the loadbalancer to talk to this service

	// maps PodIP to pod
	trackedPods map[string]api.PodStatus
}

// monitors an arbitrary set of pods when it detects
// a change in the monitored pods it rewrites haproxy
// config and invokes haproxy with the -sf option to trigger
// a config reload.
func main() {
	var usePBR = flag.Bool("usePBR", false, "Indicates if path based routing should be used. Default is to use SNI")
	var svcPort = flag.Int("svcPort", 443, "Port to route to for backend services")
	var pollInterval = flag.Int("pollInterval", 30, "Worker poll interval in seconds")
	var lb *loadbalancer
	flag.Parse()

	pollIntervalTime := time.Duration(*pollInterval) * time.Second

	shutdownMainCh := make(chan struct{})
	updateCh := make(chan []api.Pod)

	kube_client, err := client.NewInCluster()
	if err != nil {
		glog.Fatalf("Unable to connect to kubernetes api server: %v", err)
	}

	lb = newLb(*usePBR, *svcPort)
	go lb.monitorPods(pollIntervalTime, kube_client, updateCh, shutdownMainCh)

	// listen for SIGTERM
	go listenForTermination(shutdownMainCh)
	for {
		select {
		// gets name of updated service from updateCh
		case pods := <-updateCh:
			glog.Info("change in pods detected. updating config")
			// rewrite config + invoke haproxy to reload config
			err := lb.rewriteConfig(pods)
			if err != nil {
				glog.Warning(err)
			}
			// invoke haproxy to reload config
			err = notifyHAProxy()
			if err != nil {
				glog.Warning(err)
			}
		// if listenForTermination detects SIGTERM it will place a
		// value in this channel to indicate process should shutdown
		case <-shutdownMainCh:
			// close lb.shutdownCh will trigger all Asg monitors to exit
			close(lb.shutdownCh)
			return
		}
	}
}

func getPodRoute(pod api.Pod, usePBR bool) string {
	if usePBR {
		return pod.ObjectMeta.Labels[HAPROXY_PATH_NAME]
	}
	return pod.ObjectMeta.Labels[HAPROXY_NAME]
}

func getRunningPods(pods []api.Pod) []api.Pod {
	runningPods := []api.Pod{}
	for _, pod := range pods {
		_, ok := pod.ObjectMeta.Labels[HAPROXY_NAME]
		if pod.Status.Phase == api.PodRunning && ok {
			runningPods = append(runningPods, pod)
		}
	}
	return runningPods
}

// monitors pods in this k8s instance and triggers an update when
// the pods change
func (lb *loadbalancer) monitorPods(interval time.Duration, kube_client *client.Client, updateCh chan []api.Pod, shutdownCh chan struct{}) {
	// send first update immediately
	podlist, err := kube_client.Pods(api.NamespaceAll).List(labels.Everything(), fields.Everything())
	if err != nil {
		glog.Warning(err)
	}
	if lb.checkForUpdate(podlist.Items) {
		updateCh <- getRunningPods(podlist.Items)
	}

	// sit in loop forever checking for updates
	for {
		select {
		// add random duration to interval to help prevent monitors from syncing up
		// which may or may not be important..
		case <-time.After(interval + (time.Duration(rand.Intn(10)) * time.Second)):
			// get up to date pods
			podlist, err := kube_client.Pods(api.NamespaceAll).List(labels.Everything(), fields.Everything())
			if err != nil {
				// probably don't want this routine to die on failed lookup
				glog.Warning(err)
			} else {
				// only update if pods are found and no error is
				// returned from the api call to kubernetes
				if lb.checkForUpdate(podlist.Items) {
					updateCh <- getRunningPods(podlist.Items)
				}
			}
		case <-shutdownCh:
			return
		}
	}
}

// check if contents of old and new slices are the same
// returns true if there is a change
func (lb *loadbalancer) checkForUpdate(pods []api.Pod) bool {
	updated := false
	pods = getRunningPods(pods)
	if len(pods) == 0 {
		glog.Warning("No running pods found")
		return false
	}

	// check each pod returned by the api call
	for _, untrackedPod := range pods {
		if untrackedPod.Status.PodIP == "" {
			continue
		}
		// check if the untrackedPod is a for an untracked service
		if monitoredService, ok := lb.services[getPodRoute(untrackedPod, lb.usePBR)]; ok {
			tracked := false
			// check if this podIP is already tracked
			if _, ok := monitoredService.trackedPods[untrackedPod.Status.PodIP]; ok {
				tracked = true
			}
			// if untrackedPod is not tracked, append it to the list of tracked pods
			if !tracked {
				updated = true
			}
		} else {
			// if untrackedPod is not already tracked, create a new service entry
			// and set updated to true
			sv := serviceFromPod(untrackedPod, lb.usePBR)
			if sv.sni != "" {
				// update lbConfigWriter with new service
				lb.services[getPodRoute(untrackedPod, lb.usePBR)] = sv
				//lbServer := getLoadBalancerComponent(sv)
				//lb.lbConfigWriter.AddLbServer(lbServer)
				glog.Infof("New service detected with path: %s", sv.sni)
				updated = true
			}
		}
	}

	// check if any services no longer have alive pods
	removed := lb.checkForDeadPods(pods)
	return removed || updated
}

// remove pods being tracked that were not included in
// the result of the most recent API call.
func (lb *loadbalancer) checkForDeadPods(pods []api.Pod) bool {
	updated := false
	for _, svc := range lb.services {
		newTrackedPods := make(map[string]api.PodStatus)
		for _, pod := range pods {
			if trackedPod, ok := svc.trackedPods[pod.Status.PodIP]; ok {
				if svc.sni == getPodRoute(pod, lb.usePBR) {
					newTrackedPods[pod.Status.PodIP] = trackedPod
				}
			}
		}
		// if this service no longer has any pods associated with it
		if len(newTrackedPods) == 0 {
			glog.Infof("service with sni: %s is at 0 pods. Removing from config", svc.sni)
			delete(lb.services, svc.sni)
			updated = true
		} else if !reflect.DeepEqual(svc.trackedPods, newTrackedPods) {
			// if the updated list of pods doesnt match the already tracked pods
			// remove tracked pods that no longer exist
			glog.Infof("A pod has been removed from service with sni: %s", svc.sni)
			updated = true
		}
	}
	return updated
}

// rewrites lbconfig struct after a change in pods is detected
// updates then calls LbConfigWriter.WriteConfigFile()
func (lb *loadbalancer) rewriteConfig(pods []api.Pod) error {
	glog.Info("rewriting blb config")
	var newBackend Backend
	// reset backends
	lb.lbConfigWriter.SetBackends(make(map[string]Backend))
	// reset servers
	lb.lbConfigWriter.ClearLbServers()

	// for each service check for pods whose sni matches the service's
	for _, svc := range lb.services {
		lbServer := getLoadBalancerComponent(svc)

		servers := []*Server{}
		servicePods := map[string]api.PodStatus{}
		for _, pod := range pods {
			// if pod matches the route of svc append to server list
			if getPodRoute(pod, lb.usePBR) == svc.sni {
				servicePods[pod.Status.PodIP] = pod.Status
				servers = append(servers, &Server{
					Host: pod.Status.PodIP,
					Port: lb.svcPort,
				})
			}
		}
		// update service pods
		glog.Infof("new service pods: %v", servicePods)
		svc.trackedPods = servicePods

		if len(servers) > 0 {
			lb.lbConfigWriter.AddLbServer(lbServer)
			glog.Infof("found %d pods with sni %s", len(servers), svc.sni)
			if lb.usePBR {
				newBackend = Backend{
					Name:    svc.sni,
					SNI:     fmt.Sprintf("%s", svc.sni),
					Servers: servers,
				}
			} else {
				newBackend = Backend{
					Name:    svc.sni,
					SNI:     fmt.Sprintf("%s.", svc.sni),
					Servers: servers,
				}
			}
			lb.lbConfigWriter.SetBackend(svc.sni, newBackend)
			svc.trackedPods = servicePods
		}
	}

	return lb.lbConfigWriter.WriteConfigFile()
}

// invokes haproxy to trigger config reload
func notifyHAProxy() error {
	var cmd *exec.Cmd
	count := 0
	if !validConfig() {
		return fmt.Errorf("Invalid config was written. Not invoking haproxy")
	}
	pidString := getPid()
	if pidString == "" {
		cmd = exec.Command("/usr/sbin/haproxy", "-f", HAPROXY_CONFIG_PATH, "-p", HAPROXY_PID_FILE)
	} else {
		cmd = exec.Command("/usr/sbin/haproxy", "-f", HAPROXY_CONFIG_PATH, "-p", HAPROXY_PID_FILE, "-sf", pidString)
	}
	err := cmd.Run()
	for err != nil {
		count += 1
		if count > MAX_RETRIES {
			break
		}
		time.Sleep(time.Duration(count) * time.Second)
		err = cmd.Run()
		glog.Warning(err)
	}
	if err != nil {
		return err
	}
	if pidString == "" {
		return nil
	}
	pid, err := strconv.Atoi(strings.Replace(pidString, "\n", "", -1))
	if err != nil {
		return err
	}
	return killDefunctProcess(pid)
}

// zombie processes occur after haproxy reloads config
// because haproxy forks and the monitor cant track the
// grandchild process. This method find the zombie haproxy
// process and calls wait() which allows the zombie to die
func killDefunctProcess(pid int) error {
	errCh := make(chan error)
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// call Wait() on old pid. If process doesn't die
	// after 3 seconds kill it
	glog.Infof("waiting old pid %d", pid)
	go waitTimeout(proc, errCh)
	select {
	case err = <-errCh:
		if err != nil {
			glog.Warning(err)
		}
	case <-time.After(3 * time.Second):
		glog.Infof("Wait pid timed out. Killing pid %s", pid)
		return proc.Kill()
	}
	return err
}

func waitTimeout(proc *os.Process, errCh chan error) {
	state, err := proc.Wait()
	if !state.Exited() {
		glog.Warning(state.String())
	}
	errCh <- err
}

func validConfig() bool {
	cmd := exec.Command("/usr/sbin/haproxy", "-f", HAPROXY_CONFIG_PATH, "-c")
	err := cmd.Run()
	if err != nil {
		return false
	}
	return true
}

// reads pidFile. If pidFile is empty retry
// with a linear backoff
func getPid() string {
	pidBytes, err := ioutil.ReadFile(HAPROXY_PID_FILE)
	if err != nil {
		glog.Warning("Failed to read pid file")
	}
	glog.Infof("got haproxy pid: %s", string(pidBytes))
	return string(pidBytes)
}

func getSyslogAddr() string {
	cmd := exec.Command("ip", "route", "list", "0.0.0.0/0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		glog.Warning("failed to cat route file when getting syslogAddr")
		return ""
	}
	return ipFromRoute(string(out))
}

func ipFromRoute(contents string) string {
	if len(strings.Split(contents, " ")) > 2 {
		return strings.Split(contents, " ")[2]
	}
	return ""
}

// create service for a given pod
func serviceFromPod(pod api.Pod, usePBR bool) *svc {
	return &svc{
		// sni must be service specific
		sni:         getPodRoute(pod, usePBR),
		trackedPods: map[string]api.PodStatus{pod.Status.PodIP: pod.Status},
	}
}

// create haproxy Backend and LBServer struct for a given service
// each service will have its own LBServer listen on a config-specified
// port and a Backend which lists servers in the autoscaling group associated
// with that service
func getLoadBalancerComponent(sv *svc) LBServer {
	return LBServer{
		BackendName: sv.sni,
	}
}

// On SIGTERM signals monitor routines to shutdown
// and triggers process to exit cleanly
func listenForTermination(shutdownCh chan struct{}) {
	signalCh := make(chan os.Signal, 2)
	signal.Notify(signalCh, syscall.SIGTERM, os.Interrupt)
	<-signalCh
	glog.Info("Received signal. Exiting")
	close(shutdownCh)
}
