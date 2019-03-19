// Copyright (c) 2018 Zededa, Inc.
// All rights reserved.

// Utility to dump diagnostic information about connectivity

package diag

import (
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"github.com/eriknordmark/ipinfo"
	"github.com/google/go-cmp/cmp"
	log "github.com/sirupsen/logrus"
	"github.com/zededa/go-provision/agentlog"
	"github.com/zededa/go-provision/cast"
	"github.com/zededa/go-provision/devicenetwork"
	"github.com/zededa/go-provision/hardware"
	"github.com/zededa/go-provision/pubsub"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/zedcloud"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	agentName       = "diag"
	tmpDirname      = "/var/tmp/zededa"
	AADirname       = tmpDirname + "/AssignableAdapters"
	DNCDirname      = tmpDirname + "/DeviceNetworkConfig"
	identityDirname = "/config"
	selfRegFile     = identityDirname + "/self-register-failed"
	serverFileName  = identityDirname + "/server"
	deviceCertName  = identityDirname + "/device.cert.pem"
	deviceKeyName   = identityDirname + "/device.key.pem"
	onboardCertName = identityDirname + "/onboard.cert.pem"
	onboardKeyName  = identityDirname + "/onboard.key.pem"
	maxRetries      = 5
)

// State passed to handlers
type diagContext struct {
	devicenetwork.DeviceNetworkContext
	DevicePortConfigList    *types.DevicePortConfigList
	forever                 bool // Keep on reporting until ^C
	pacContents             bool // Print PAC file contents
	ledCounter              int  // Supress work and output
	subGlobalConfig         *pubsub.Subscription
	subLedBlinkCounter      *pubsub.Subscription
	subDeviceNetworkStatus  *pubsub.Subscription
	subDevicePortConfigList *pubsub.Subscription
	gotBC                   bool
	gotDNS                  bool
	gotDPCList              bool
	serverNameAndPort       string
	serverName              string // Without port number
	zedcloudCtx             *zedcloud.ZedCloudContext
	cert                    *tls.Certificate
}

// Set from Makefile
var Version = "No version specified"

var debug = false
var debugOverride bool // From command line arg
var simulateDnsFailure = false
var simulatePingFailure = false

func Run() {
	versionPtr := flag.Bool("v", false, "Version")
	debugPtr := flag.Bool("d", false, "Debug flag")
	curpartPtr := flag.String("c", "", "Current partition")
	stdoutPtr := flag.Bool("s", false, "Use stdout")
	foreverPtr := flag.Bool("f", false, "Forever flag")
	pacContentsPtr := flag.Bool("p", false, "Print PAC file contents")
	simulateDnsFailurePtr := flag.Bool("D", false, "simulateDnsFailure flag")
	simulatePingFailurePtr := flag.Bool("P", false, "simulatePingFailure flag")
	flag.Parse()
	debug = *debugPtr
	debugOverride = debug
	if debugOverride {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}
	curpart := *curpartPtr
	useStdout := *stdoutPtr
	simulateDnsFailure = *simulateDnsFailurePtr
	simulatePingFailure = *simulatePingFailurePtr
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	logf, err := agentlog.Init(agentName, curpart)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()

	if useStdout {
		multi := io.MultiWriter(logf, os.Stdout)
		log.SetOutput(multi)
	}

	ctx := diagContext{
		forever:     *foreverPtr,
		pacContents: *pacContentsPtr,
	}
	ctx.DeviceNetworkStatus = &types.DeviceNetworkStatus{}
	ctx.DevicePortConfigList = &types.DevicePortConfigList{}

	// XXX should we subscribe to and get GlobalConfig for debug??

	server, err := ioutil.ReadFile(serverFileName)
	if err != nil {
		log.Fatal(err)
	}
	ctx.serverNameAndPort = strings.TrimSpace(string(server))
	ctx.serverName = strings.Split(ctx.serverNameAndPort, ":")[0]

	zedcloudCtx := zedcloud.ZedCloudContext{
		DeviceNetworkStatus: ctx.DeviceNetworkStatus,
		FailureFunc:         zedcloud.ZedCloudFailure,
		SuccessFunc:         zedcloud.ZedCloudSuccess,
	}
	if fileExists(deviceCertName) && fileExists(deviceKeyName) {
		cert, err := tls.LoadX509KeyPair(deviceCertName,
			deviceKeyName)
		if err != nil {
			log.Fatal(err)
		}
		ctx.cert = &cert
	} else if fileExists(onboardCertName) && fileExists(onboardKeyName) {
		cert, err := tls.LoadX509KeyPair(onboardCertName,
			onboardKeyName)
		if err != nil {
			log.Fatal(err)
		}
		ctx.cert = &cert
		fmt.Printf("WARNING: no device cert; using onboarding cert at %v\n",
			time.Now().Format(time.RFC3339Nano))

	} else {
		fmt.Printf("ERROR: no device cert and no onboarding cert at %v\n",
			time.Now().Format(time.RFC3339Nano))
		os.Exit(1)
	}

	tlsConfig, err := zedcloud.GetTlsConfig(ctx.serverName, ctx.cert)
	if err != nil {
		log.Fatal(err)
	}
	zedcloudCtx.TlsConfig = tlsConfig
	ctx.zedcloudCtx = &zedcloudCtx

	subLedBlinkCounter, err := pubsub.Subscribe("", types.LedBlinkCounter{},
		false, &ctx)
	if err != nil {
		errStr := fmt.Sprintf("ERROR: internal Subscribe failed %s\n", err)
		panic(errStr)
	}
	subLedBlinkCounter.ModifyHandler = handleLedBlinkModify
	ctx.subLedBlinkCounter = subLedBlinkCounter
	subLedBlinkCounter.Activate()

	subDeviceNetworkStatus, err := pubsub.Subscribe("nim",
		types.DeviceNetworkStatus{}, false, &ctx)
	if err != nil {
		errStr := fmt.Sprintf("ERROR: internal Subscribe failed %s\n", err)
		panic(errStr)
	}
	subDeviceNetworkStatus.ModifyHandler = handleDNSModify
	subDeviceNetworkStatus.DeleteHandler = handleDNSDelete
	ctx.subDeviceNetworkStatus = subDeviceNetworkStatus
	subDeviceNetworkStatus.Activate()

	subDevicePortConfigList, err := pubsub.SubscribePersistent("nim",
		types.DevicePortConfigList{}, false, &ctx)
	if err != nil {
		errStr := fmt.Sprintf("ERROR: internal Subscribe failed %s\n", err)
		panic(errStr)
	}
	subDevicePortConfigList.ModifyHandler = handleDPCModify
	ctx.subDevicePortConfigList = subDevicePortConfigList
	subDevicePortConfigList.Activate()

	for {
		select {
		case change := <-subLedBlinkCounter.C:
			ctx.gotBC = true
			subLedBlinkCounter.ProcessChange(change)

		case change := <-subDeviceNetworkStatus.C:
			ctx.gotDNS = true
			subDeviceNetworkStatus.ProcessChange(change)

		case change := <-subDevicePortConfigList.C:
			ctx.gotDPCList = true
			subDevicePortConfigList.ProcessChange(change)
		}
		if !ctx.forever && ctx.gotDNS && ctx.gotBC && ctx.gotDPCList {
			break
		}
	}
}

func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

func DNCExists(model string) bool {
	DNCFilename := fmt.Sprintf("%s/%s.json", DNCDirname, model)
	return fileExists(DNCFilename)
}

func AAExists(model string) bool {
	AAFilename := fmt.Sprintf("%s/%s.json", AADirname, model)
	return fileExists(AAFilename)
}

func handleLedBlinkModify(ctxArg interface{}, key string,
	configArg interface{}) {

	config := cast.CastLedBlinkCounter(configArg)
	ctx := ctxArg.(*diagContext)

	if key != "ledconfig" {
		log.Errorf("handleLedBlinkModify: ignoring %s\n", key)
		return
	}
	// Supress work and logging if no change
	if config.BlinkCounter == ctx.ledCounter {
		return
	}
	ctx.ledCounter = config.BlinkCounter
	printOutput(ctx)
}

func handleDNSModify(ctxArg interface{}, key string, statusArg interface{}) {

	status := cast.CastDeviceNetworkStatus(statusArg)
	ctx := ctxArg.(*diagContext)
	if key != "global" {
		log.Infof("handleDNSModify: ignoring %s\n", key)
		return
	}
	log.Infof("handleDNSModify for %s\n", key)
	if status.Testing {
		log.Infof("handleDNSModify ignoring Testing\n")
		return
	}
	if cmp.Equal(ctx.DeviceNetworkStatus, status) {
		log.Infof("handleDNSModify unchanged\n")
		return
	}
	log.Infof("handleDNSModify: changed %v",
		cmp.Diff(ctx.DeviceNetworkStatus, status))
	*ctx.DeviceNetworkStatus = status
	// XXX can we limit to interfaces which changed?
	printOutput(ctx)
	log.Infof("handleDNSModify done for %s\n", key)
}

func handleDNSDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Infof("handleDNSDelete for %s\n", key)
	ctx := ctxArg.(*diagContext)

	if key != "global" {
		log.Infof("handleDNSDelete: ignoring %s\n", key)
		return
	}
	*ctx.DeviceNetworkStatus = types.DeviceNetworkStatus{}
	printOutput(ctx)
	log.Infof("handleDNSDelete done for %s\n", key)
}

func handleDPCModify(ctxArg interface{}, key string, statusArg interface{}) {

	status := cast.CastDevicePortConfigList(statusArg)
	ctx := ctxArg.(*diagContext)
	if key != "global" {
		log.Infof("handleDPCModify: ignoring %s\n", key)
		return
	}
	log.Infof("handleDPCModify for %s\n", key)
	if cmp.Equal(ctx.DevicePortConfigList, status) {
		return
	}
	log.Infof("handleDPCModify: changed %v",
		cmp.Diff(ctx.DevicePortConfigList, status))
	*ctx.DevicePortConfigList = status
	// XXX can we limit to interfaces which changed?
	// XXX exclude if only timestamps changed?
	printOutput(ctx)
	log.Infof("handleDPCModify done for %s\n", key)
}

// Print output for all interfaces
// XXX can we limit to interfaces which changed?
func printOutput(ctx *diagContext) {

	// Defer until we have an initial BlinkCounter and DeviceNetworkStatus
	if !ctx.gotDNS || !ctx.gotBC || !ctx.gotDPCList {
		return
	}

	fmt.Printf("\nINFO: updated diag information at %v\n",
		time.Now().Format(time.RFC3339Nano))
	savedHardwareModel := hardware.GetHardwareModelOverride()
	hardwareModel := hardware.GetHardwareModelNoOverride()
	if savedHardwareModel != "" && savedHardwareModel != hardwareModel {
		fmt.Printf("INFO: dmidecode model string %s overridden as %s\n",
			hardwareModel, savedHardwareModel)
	}
	if savedHardwareModel != "" {
		if !DNCExists(savedHardwareModel) {
			fmt.Printf("ERROR: /config/hardwaremodel %s does not exist in /var/tmp/zededa/DeviceNetworkConfig\n",
				savedHardwareModel)
			fmt.Printf("NOTE: Device is using /var/tmp/zededa/DeviceNetworkConfig/default.json\n")
		}
		if !AAExists(savedHardwareModel) {
			fmt.Printf("ERROR: /config/hardwaremodel %s does not exist in /var/tmp/zededa/AssignableAdapters\n",
				savedHardwareModel)
			fmt.Printf("NOTE: Device is using /var/tmp/zededa/AssignableAdapters/default.json\n")
		}
	}
	if !DNCExists(hardwareModel) {
		fmt.Printf("INFO: dmidecode model %s does not exist in /var/tmp/zededa/DeviceNetworkConfig\n",
			hardwareModel)
	}
	if !AAExists(hardwareModel) {
		fmt.Printf("INFO: dmidecode model %s does not exist in /var/tmp/zededa/AssignableAdapters\n",
			hardwareModel)
	}
	// XXX certificate fingerprints? What does zedcloud use?
	if fileExists(selfRegFile) {
		fmt.Printf("INFO: selfRegister is still in progress\n")
		// XXX print onboarding cert
	}

	switch ctx.ledCounter {
	case 0:
		fmt.Printf("ERROR: Summary: Unknown LED counter 0\n")
	case 1:
		fmt.Printf("ERROR: Summary: Waiting for DHCP IP address(es)\n")
	case 2:
		fmt.Printf("ERROR: Summary: Trying to connect to EV Controller\n")
	case 3:
		fmt.Printf("WARNING: Summary: Connected to EV Controller but not onboarded\n")
	case 4:
		fmt.Printf("INFO: Summary: Connected to EV Controller and onboarded\n")
	case 10:
		fmt.Printf("ERROR: Summary: Onboarding failure or conflict\n")
	case 11:
		fmt.Printf("ERROR: Summary: Missing /var/tmp/zededa/DeviceNetworkConfig/ model file\n")
	case 12:
		fmt.Printf("ERROR: Summary: Response without TLS - ignored\n")
	case 13:
		fmt.Printf("ERROR: Summary: Response without OSCP or bad OSCP - ignored\n")
	default:
		fmt.Printf("ERROR: Summary: Unsupported LED counter %d\n",
			ctx.ledCounter)
	}

	// Print info about fallback
	DPCLen := len(ctx.DevicePortConfigList.PortConfigList)
	if DPCLen > 0 {
		first := ctx.DevicePortConfigList.PortConfigList[0]
		if ctx.DevicePortConfigList.CurrentIndex != 0 {
			fmt.Printf("WARNING: Not using highest priority DevicePortConfig key %s due to %s\n",
				first.Key, first.LastError)
			for i, dpc := range ctx.DevicePortConfigList.PortConfigList {
				if i == 0 {
					continue
				}
				if i != ctx.DevicePortConfigList.CurrentIndex {
					fmt.Printf("WARNING: Not using priority %d DevicePortConfig key %s due to %s\n",
						i, dpc.Key, dpc.LastError)
				} else {
					fmt.Printf("INFO: Using priority %d DevicePortConfig key %s\n",
						i, dpc.Key)
					break
				}
			}
			if DPCLen-1 > ctx.DevicePortConfigList.CurrentIndex {
				fmt.Printf("INFO: Have %d backup DevicePortConfig\n",
					DPCLen-1-ctx.DevicePortConfigList.CurrentIndex)
			}
		} else {
			fmt.Printf("INFO: Using highest priority DevicePortConfig key %s\n",
				first.Key)
			if DPCLen > 1 {
				fmt.Printf("INFO: Have %d backup DevicePortConfig\n",
					DPCLen-1)
			}
		}
	}
	numPorts := len(ctx.DeviceNetworkStatus.Ports)
	mgmtPorts := 0
	passPorts := 0
	passOtherPorts := 0

	// XXX add to DeviceNetworkStatus?
	// fmt.Printf("DEBUG: Using DevicePortConfig key %s prio %s lastSucceeded %v\n",
	// 	ctx.DeviceNetworkStatus.Key, ctx.DeviceNetworkStatus.TimePriority,
	//	ctx.DeviceNetworkStatus.LastSucceeded)
	numMgmtPorts := len(types.GetMgmtPortsAny(*ctx.DeviceNetworkStatus, 0))
	fmt.Printf("INFO: Have %d total ports. %d ports should be connected to EV controller\n", numPorts, numMgmtPorts)
	for _, port := range ctx.DeviceNetworkStatus.Ports {
		// Print usefully formatted info based on which
		// fields are set and Dhcp type; proxy info order
		ifname := port.IfName
		isMgmt := false
		isFree := false
		if types.IsFreeMgmtPort(*ctx.DeviceNetworkStatus, ifname) {
			isMgmt = true
			isFree = true
		} else if types.IsMgmtPort(*ctx.DeviceNetworkStatus, ifname) {
			isMgmt = true
		}
		if isMgmt {
			mgmtPorts += 1
		}

		typeStr := "for application use"
		if isFree {
			typeStr = "for EV Controller without usage-based charging"
		} else if isMgmt {
			typeStr = "for EV Controller"
		}
		fmt.Printf("INFO: Port %s: %s\n", ifname, typeStr)
		ipCount := 0
		for _, ai := range port.AddrInfoList {
			if ai.Addr.IsLinkLocalUnicast() {
				continue
			}
			ipCount += 1
			noGeo := ipinfo.IPInfo{}
			if ai.Geo == noGeo {
				fmt.Printf("INFO: %s: IP address %s not geolocated\n",
					ifname, ai.Addr)
			} else {
				fmt.Printf("INFO: %s: IP address %s geolocated to %+v\n",
					ifname, ai.Addr, ai.Geo)
			}
		}
		if ipCount == 0 {
			fmt.Printf("INFO: %s: No IP address\n",
				ifname)
		}

		fmt.Printf("INFO: %s: DNS servers: ", ifname)
		for _, ds := range port.DnsServers {
			fmt.Printf("%s, ", ds.String())
		}
		fmt.Printf("\n")
		// If static print static config
		if port.Dhcp == types.DT_STATIC {
			fmt.Printf("INFO: %s: Static IP subnet: %s\n",
				ifname, port.Subnet.String())
			fmt.Printf("INFO: %s: Static IP router: %s\n",
				ifname, port.Gateway.String())
			fmt.Printf("INFO: %s: Static Domain Name: %s\n",
				ifname, port.DomainName)
			fmt.Printf("INFO: %s: Static NTP server: %s\n",
				ifname, port.NtpServer.String())
		}
		printProxy(ctx, port, ifname)

		if !isMgmt {
			fmt.Printf("INFO: %s: not intended for EV controller; skipping those tests\n",
				ifname)
			continue
		}
		if ipCount == 0 {
			fmt.Printf("WARNING: %s: No IP address to connect to EV controller\n",
				ifname)
			continue
		}
		// DNS lookup, ping and getUuid calls
		if !tryLookupIP(ctx, ifname) {
			continue
		}
		if !tryPing(ctx, ifname, "") {
			fmt.Printf("ERROR: %s: ping failed to %s; trying google\n",
				ifname, ctx.serverNameAndPort)
			origServerName := ctx.serverName
			origServerNameAndPort := ctx.serverNameAndPort
			ctx.serverName = "www.google.com"
			ctx.serverNameAndPort = ctx.serverName
			res := tryPing(ctx, ifname, "http://www.google.com")
			if res {
				fmt.Printf("WARNING: %s: Can reach http://google.com but not https://%s\n",
					ifname, origServerNameAndPort)
			} else {
				fmt.Printf("ERROR: %s: Can't reach http://google.com; likely lack of Internet connectivity\n",
					ifname)
			}
			res = tryPing(ctx, ifname, "https://www.google.com")
			if res {
				fmt.Printf("WARNING: %s: Can reach https://google.com but not https://%s\n",
					ifname, origServerNameAndPort)
			} else {
				fmt.Printf("ERROR: %s: Can't reach https://google.com; likely lack of Internet connectivity\n",
					ifname)
			}
			ctx.serverName = origServerName
			ctx.serverNameAndPort = origServerNameAndPort
			// restore TLS
			tlsConfig, err := zedcloud.GetTlsConfig(ctx.serverName,
				ctx.cert)
			if err != nil {
				errStr := fmt.Sprintf("ERROR: %s: internal GetTlsConfig failed %s\n",
					ifname, err)
				panic(errStr)
			}
			ctx.zedcloudCtx.TlsConfig = tlsConfig
			continue
		}
		if !tryGetUuid(ctx, ifname) {
			continue
		}
		if isMgmt {
			passPorts += 1
		} else {
			passOtherPorts += 1
		}
		fmt.Printf("PASS: port %s fully connected to EV controller %s\n",
			ifname, ctx.serverName)
	}
	if passOtherPorts > 0 {
		fmt.Printf("WARNING: %d non-management ports have connectivity to the EV controller. Is that intentional?\n", passOtherPorts)
	}
	if mgmtPorts == 0 {
		fmt.Printf("ERROR: No ports specified to have EV controller connectivity\n")
	} else if passPorts == mgmtPorts {
		fmt.Printf("PASS: All ports specified to have EV controller connectivity passed test\n")
	} else {
		fmt.Printf("WARNING: %d out of %d ports specified to have EV controller connectivity passed test\n",
			passPorts, mgmtPorts)
	}
}

func printProxy(ctx *diagContext, port types.NetworkPortStatus,
	ifname string) {

	if devicenetwork.IsProxyConfigEmpty(port.ProxyConfig) {
		fmt.Printf("INFO: %s: no http(s) proxy\n", ifname)
		return
	}
	if port.ProxyConfig.Exceptions != "" {
		fmt.Printf("INFO: %s: proxy exceptions %s\n",
			ifname, port.ProxyConfig.Exceptions)
	}
	if port.Error != "" {
		fmt.Printf("ERROR: %s: from WPAD? %s\n", ifname, port.Error)
	}
	if port.ProxyConfig.NetworkProxyEnable {
		if port.ProxyConfig.NetworkProxyURL == "" {
			if port.ProxyConfig.WpadURL == "" {
				fmt.Printf("WARNING: %s: WPAD enabled but found no URL\n",
					ifname)
			} else {
				fmt.Printf("INFO: %s: WPAD enabled found URL %s\n",
					ifname, port.ProxyConfig.WpadURL)
			}
		} else {
			fmt.Printf("INFO: %s: WPAD fetched from %s\n",
				ifname, port.ProxyConfig.NetworkProxyURL)
		}
	}
	pacLen := len(port.ProxyConfig.Pacfile)
	if pacLen > 0 {
		fmt.Printf("INFO: %s: Have PAC file len %d\n",
			ifname, pacLen)
		if ctx.pacContents {
			pacFile, err := base64.StdEncoding.DecodeString(port.ProxyConfig.Pacfile)
			if err != nil {
				errStr := fmt.Sprintf("Decoding proxy file failed: %s", err)
				log.Errorf(errStr)
			} else {
				fmt.Printf("INFO: %s: PAC file:\n%s\n",
					ifname, pacFile)
			}
		}
	} else {
		for _, proxy := range port.ProxyConfig.Proxies {
			switch proxy.Type {
			case types.NPT_HTTP:
				var httpProxy string
				if proxy.Port > 0 {
					httpProxy = fmt.Sprintf("%s:%d", proxy.Server, proxy.Port)
				} else {
					httpProxy = fmt.Sprintf("%s", proxy.Server)
				}
				fmt.Printf("INFO: %s: http proxy %s\n",
					ifname, httpProxy)
			case types.NPT_HTTPS:
				var httpsProxy string
				if proxy.Port > 0 {
					httpsProxy = fmt.Sprintf("%s:%d", proxy.Server, proxy.Port)
				} else {
					httpsProxy = fmt.Sprintf("%s", proxy.Server)
				}
				fmt.Printf("INFO: %s: https proxy %s\n",
					ifname, httpsProxy)
			}
		}
	}
}

// XXX should we make this and send.go use DNS on one interface?
func tryLookupIP(ctx *diagContext, ifname string) bool {

	ips, err := net.LookupIP(ctx.serverName)
	if err != nil {
		fmt.Printf("ERROR: %s: DNS lookup of %s failed: %s\n",
			ifname, ctx.serverName, err)
		return false
	}
	if len(ips) == 0 {
		fmt.Printf("ERROR: %s: DNS lookup of %s returned no answers\n",
			ifname, ctx.serverName)
		return false
	}
	for _, ip := range ips {
		fmt.Printf("INFO: %s: DNS lookup of %s returned %s\n",
			ifname, ctx.serverName, ip.String())
	}
	if simulateDnsFailure {
		fmt.Printf("INFO: %s: Simulate DNS lookup failure\n", ifname)
		return false
	}
	return true
}

func tryPing(ctx *diagContext, ifname string, requrl string) bool {

	zedcloudCtx := ctx.zedcloudCtx
	if requrl == "" {
		requrl = ctx.serverNameAndPort + "/api/v1/edgedevice/ping"
	} else {
		tlsConfig, err := zedcloud.GetTlsConfig(ctx.serverName,
			ctx.cert)
		if err != nil {
			errStr := fmt.Sprintf("ERROR: %s: internal GetTlsConfig failed %s\n",
				ifname, err)
			panic(errStr)
		}
		zedcloudCtx.TlsConfig = tlsConfig
		tlsConfig.InsecureSkipVerify = true
	}

	// As we ping the cloud or other URLs, don't affect the LEDs
	zedcloudCtx.NoLedManager = true

	retryCount := 0
	done := false
	var delay time.Duration
	for !done {
		time.Sleep(delay)
		done, _, _ = myGet(zedcloudCtx, requrl, ifname, retryCount)
		if done {
			break
		}
		retryCount += 1
		if maxRetries != 0 && retryCount > maxRetries {
			fmt.Printf("ERROR: %s: Exceeded %d retries for ping\n",
				ifname, maxRetries)
			return false
		}
		delay = time.Second
	}
	if simulatePingFailure {
		fmt.Printf("INFO: %s: Simulate ping failure\n", ifname)
		return false
	}
	return true
}

func tryGetUuid(ctx *diagContext, ifname string) bool {

	zedcloudCtx := ctx.zedcloudCtx
	requrl := ctx.serverNameAndPort + "/api/v1/edgedevice/config"
	// As we ping the cloud or other URLs, don't affect the LEDs
	zedcloudCtx.NoLedManager = true
	retryCount := 0
	done := false
	var delay time.Duration
	for !done {
		time.Sleep(delay)
		done, _, _ = myGet(zedcloudCtx, requrl, ifname, retryCount)
		if done {
			break
		}
		retryCount += 1
		if maxRetries != 0 && retryCount > maxRetries {
			fmt.Printf("ERROR: %s: Exceeded %d retries for get config\n",
				ifname, maxRetries)
			return false
		}
		delay = time.Second
	}
	return true
}

// Get something without a return type; used by ping
// Returns true when done; false when retry.
// Returns the response when done. Caller can not use resp.Body but
// can use the contents []byte
func myGet(zedcloudCtx *zedcloud.ZedCloudContext, requrl string, ifname string,
	retryCount int) (bool, *http.Response, []byte) {

	var preqUrl string
	if strings.HasPrefix(requrl, "http:") {
		preqUrl = requrl
	} else if strings.HasPrefix(requrl, "https:") {
		preqUrl = requrl
	} else {
		preqUrl = "https://" + requrl
	}
	proxyUrl, err := zedcloud.LookupProxy(zedcloudCtx.DeviceNetworkStatus,
		ifname, preqUrl)
	if err != nil {
		fmt.Printf("ERROR: %s: LookupProxy failed: %s\n", ifname, err)
	} else if proxyUrl != nil {
		fmt.Printf("INFO: %s: Using proxy %s to reach %s\n",
			ifname, proxyUrl.String(), requrl)
	}
	const allowProxy = true
	resp, contents, err := zedcloud.SendOnIntf(*zedcloudCtx,
		requrl, ifname, 0, nil, allowProxy, 15)
	if err != nil {
		fmt.Printf("ERROR: %s: get %s failed: %s\n",
			ifname, requrl, err)
		return false, nil, nil
	}

	switch resp.StatusCode {
	case http.StatusOK:
		fmt.Printf("INFO: %s: %s StatusOK\n", ifname, requrl)
		return true, resp, contents
	default:
		fmt.Printf("ERROR: %s: %s statuscode %d %s\n",
			ifname, requrl, resp.StatusCode,
			http.StatusText(resp.StatusCode))
		fmt.Printf("ERRROR: %s: Received %s\n",
			ifname, string(contents))
		return false, nil, nil
	}
}
