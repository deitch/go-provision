// Copyright (c) 2018 Zededa, Inc.
// All rights reserved.

//watcher tells ledmanager about
//change in ledmanager config file,
//which contains number of times
//LED has to blink on any device
//ledmanager notify each event by
//triggering blink on device.
//number of blink is equal to
//blink counter received by status
//file...
//After each blink we will take
//pause of 200ms.
//After end of each event we will take
//pause of 1200ms...

package ledmanager

import (
	"flag"
	"fmt"
	"github.com/google/go-cmp/cmp"
	log "github.com/sirupsen/logrus"
	"github.com/zededa/go-provision/agentlog"
	"github.com/zededa/go-provision/cast"
	"github.com/zededa/go-provision/hardware"
	"github.com/zededa/go-provision/pidfile"
	"github.com/zededa/go-provision/pubsub"
	"github.com/zededa/go-provision/types"
	"io/ioutil"
	"os"
	"os/exec"
	"time"
)

const (
	agentName        = "ledmanager"
	ledConfigDirName = "/var/tmp/ledmanager/config"
)

// State passed to handlers
type ledManagerContext struct {
	countChange            chan int
	ledCounter             int // Supress work and logging if no change
	subGlobalConfig        *pubsub.Subscription
	subLedBlinkCounter     *pubsub.Subscription
	subDeviceNetworkStatus *pubsub.Subscription
	deviceNetworkStatus    types.DeviceNetworkStatus
	usableAddressCount     int
	derivedLedCounter      int // Based on ledCounter + usableAddressCount
}

type Blink200msFunc func()
type BlinkInitFunc func()

type modelToFuncs struct {
	model     string
	initFunc  BlinkInitFunc
	blinkFunc Blink200msFunc
}

// XXX introduce wildcard matching on model names? Just a default at the end
var mToF = []modelToFuncs{
	modelToFuncs{
		model:     "Supermicro.SYS-E100-9APP",
		blinkFunc: ExecuteDDCmd},
	modelToFuncs{
		model:     "Supermicro.SYS-E100-9S",
		blinkFunc: ExecuteDDCmd},
	modelToFuncs{
		model:     "Supermicro.SYS-E50-9AP",
		blinkFunc: ExecuteDDCmd},
	modelToFuncs{ // XXX temporary fix for old BIOS
		model:     "Supermicro.Super Server",
		blinkFunc: ExecuteDDCmd},
	modelToFuncs{
		model:     "Supermicro.SYS-E300-8D",
		blinkFunc: ExecuteDDCmd},
	modelToFuncs{
		model:     "Supermicro.SYS-E300-9A-4CN10P",
		blinkFunc: ExecuteDDCmd},
	modelToFuncs{
		model:     "Supermicro.SYS-5018D-FN8T",
		blinkFunc: ExecuteDDCmd},
	modelToFuncs{
		model:     "hisilicon,hi6220-hikey.hisilicon,hi6220.",
		initFunc:  InitWifiLedCmd,
		blinkFunc: ExecuteWifiLedCmd},
	modelToFuncs{
		model:     "hisilicon,hikey.hisilicon,hi6220.",
		initFunc:  InitWifiLedCmd,
		blinkFunc: ExecuteWifiLedCmd},
	modelToFuncs{
		model: "QEMU.Standard PC (i440FX + PIIX, 1996)",
		// No dd disk light blinking on QEMU
	},
	// Last in table as a default
	modelToFuncs{
		model:     "",
		blinkFunc: ExecuteDDCmd},
}

var debug bool
var debugOverride bool // From command line arg

// Set from Makefile
var Version = "No version specified"

func Run() {
	versionPtr := flag.Bool("v", false, "Version")
	debugPtr := flag.Bool("d", false, "Debug")
	curpartPtr := flag.String("c", "", "Current partition")
	flag.Parse()
	debug = *debugPtr
	debugOverride = debug
	if debugOverride {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}
	curpart := *curpartPtr
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	logf, err := agentlog.Init(agentName, curpart)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()

	if err := pidfile.CheckAndCreatePidfile(agentName); err != nil {
		log.Fatal(err)
	}
	log.Infof("Starting %s\n", agentName)

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	agentlog.StillRunning(agentName)

	model := hardware.GetHardwareModel()
	log.Infof("Got HardwareModel %s\n", model)

	var blinkFunc Blink200msFunc
	var initFunc BlinkInitFunc
	for _, m := range mToF {
		if m.model == model {
			blinkFunc = m.blinkFunc
			initFunc = m.initFunc
			break
		}
		if m.model == "" {
			log.Infof("No blink function for %s\n", model)
			blinkFunc = m.blinkFunc
			initFunc = m.initFunc
			break
		}
	}

	if initFunc != nil {
		initFunc()
	}

	// Any state needed by handler functions
	ctx := ledManagerContext{}
	ctx.countChange = make(chan int)
	go TriggerBlinkOnDevice(ctx.countChange, blinkFunc)

	subLedBlinkCounter, err := pubsub.Subscribe("", types.LedBlinkCounter{},
		false, &ctx)
	if err != nil {
		log.Fatal(err)
	}
	subLedBlinkCounter.ModifyHandler = handleLedBlinkModify
	subLedBlinkCounter.DeleteHandler = handleLedBlinkDelete
	ctx.subLedBlinkCounter = subLedBlinkCounter
	subLedBlinkCounter.Activate()

	subDeviceNetworkStatus, err := pubsub.Subscribe("nim",
		types.DeviceNetworkStatus{}, false, &ctx)
	if err != nil {
		log.Fatal(err)
	}
	subDeviceNetworkStatus.ModifyHandler = handleDNSModify
	subDeviceNetworkStatus.DeleteHandler = handleDNSDelete
	ctx.subDeviceNetworkStatus = subDeviceNetworkStatus
	subDeviceNetworkStatus.Activate()

	// Look for global config such as log levels
	subGlobalConfig, err := pubsub.Subscribe("", types.GlobalConfig{},
		false, &ctx)
	if err != nil {
		log.Fatal(err)
	}
	subGlobalConfig.ModifyHandler = handleGlobalConfigModify
	subGlobalConfig.DeleteHandler = handleGlobalConfigDelete
	ctx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	for {
		select {
		case change := <-subGlobalConfig.C:
			subGlobalConfig.ProcessChange(change)

		case change := <-subDeviceNetworkStatus.C:
			subDeviceNetworkStatus.ProcessChange(change)

		case change := <-subLedBlinkCounter.C:
			subLedBlinkCounter.ProcessChange(change)

		case <-stillRunning.C:
			agentlog.StillRunning(agentName)
		}
	}
}

func handleLedBlinkModify(ctxArg interface{}, key string,
	configArg interface{}) {

	config := cast.CastLedBlinkCounter(configArg)
	ctx := ctxArg.(*ledManagerContext)

	if key != "ledconfig" {
		log.Errorf("handleLedBlinkModify: ignoring %s\n", key)
		return
	}
	// Supress work and logging if no change
	if config.BlinkCounter == ctx.ledCounter {
		return
	}
	ctx.ledCounter = config.BlinkCounter
	updateDerivedLedCounter(ctx)
	log.Infof("handleLedBlinkModify done for %s\n", key)
}

// Merge the 1/2 values based on having usable addresses or not, with
// the value we get based on access to zedcloud or errors.
func updateDerivedLedCounter(ctx *ledManagerContext) {
	if ctx.usableAddressCount == 0 {
		ctx.derivedLedCounter = 1
	} else if ctx.ledCounter < 2 {
		ctx.derivedLedCounter = 2
	} else {
		ctx.derivedLedCounter = ctx.ledCounter
	}
	log.Infof("updateDerivedLedCounter counter %d usableAddr %d, derived %d\n",
		ctx.ledCounter, ctx.usableAddressCount, ctx.derivedLedCounter)
	ctx.countChange <- ctx.derivedLedCounter
}

func handleLedBlinkDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	log.Infof("handleLedBlinkDelete for %s\n", key)
	ctx := ctxArg.(*ledManagerContext)

	if key != "ledconfig" {
		log.Errorf("handleLedBlinkDelete: ignoring %s\n", key)
		return
	}
	// XXX or should we tell the blink go routine to exit?
	ctx.ledCounter = 0
	updateDerivedLedCounter(ctx)
	log.Infof("handleLedBlinkDelete done for %s\n", key)
}

func TriggerBlinkOnDevice(countChange chan int, blinkFunc Blink200msFunc) {
	var counter int
	for {
		select {
		case counter = <-countChange:
			log.Debugf("Received counter update: %d\n",
				counter)
		default:
			log.Debugf("Unchanged counter: %d\n", counter)
		}
		log.Debugln("Number of times LED will blink: ", counter)
		for i := 0; i < counter; i++ {
			if blinkFunc != nil {
				blinkFunc()
			}
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(1200 * time.Millisecond)
	}
}

func DummyCmd() {
	time.Sleep(200 * time.Millisecond)
}

// Should be tuned so that the LED lights up for 200ms
// Disable cache since there might be a filesystem on the device
func ExecuteDDCmd() {
	cmd := exec.Command("dd", "if=/dev/sda", "of=/dev/null", "bs=4M", "count=22", "iflag=nocache")
	stdout, err := cmd.Output()
	if err != nil {
		log.Errorln("dd error: ", err)
		return
	}
	log.Debugf("ddinfo: %s\n", stdout)
}

const (
	ledFilename        = "/sys/class/leds/wifi_active"
	triggerFilename    = ledFilename + "/trigger"
	brightnessFilename = ledFilename + "/brightness"
)

// Disable existimg trigger
// Write "none\n" to /sys/class/leds/wifi_active/trigger
func InitWifiLedCmd() {
	log.Infof("InitWifiLedCmd\n")
	b := []byte("none")
	err := ioutil.WriteFile(triggerFilename, b, 0644)
	if err != nil {
		log.Fatal(err, triggerFilename)
	}
}

// Enable the Wifi led for 200ms
func ExecuteWifiLedCmd() {
	b := []byte("1")
	err := ioutil.WriteFile(brightnessFilename, b, 0644)
	if err != nil {
		log.Fatal(err, brightnessFilename)
	}
	time.Sleep(200 * time.Millisecond)
	b = []byte("0")
	err = ioutil.WriteFile(brightnessFilename, b, 0644)
	if err != nil {
		log.Fatal(err, brightnessFilename)
	}
}

func handleDNSModify(ctxArg interface{}, key string, statusArg interface{}) {

	ctx := ctxArg.(*ledManagerContext)
	status := cast.CastDeviceNetworkStatus(statusArg)
	if key != "global" {
		log.Infof("handleDNSModify: ignoring %s\n", key)
		return
	}
	log.Infof("handleDNSModify for %s\n", key)
	if status.Testing {
		log.Infof("handleDNSModify ignoring Testing\n")
		return
	}
	if cmp.Equal(ctx.deviceNetworkStatus, status) {
		log.Infof("handleDNSModify no change\n")
		return
	}
	ctx.deviceNetworkStatus = status
	newAddrCount := types.CountLocalAddrAnyNoLinkLocal(ctx.deviceNetworkStatus)
	log.Infof("handleDNSModify %d usable addresses\n", newAddrCount)
	if (ctx.usableAddressCount == 0 && newAddrCount != 0) ||
		(ctx.usableAddressCount != 0 && newAddrCount == 0) {
		ctx.usableAddressCount = newAddrCount
		updateDerivedLedCounter(ctx)
	}
	log.Infof("handleDNSModify done for %s\n", key)
}

func handleDNSDelete(ctxArg interface{}, key string, statusArg interface{}) {

	ctx := ctxArg.(*ledManagerContext)
	log.Infof("handleDNSDelete for %s\n", key)
	if key != "global" {
		log.Infof("handleDNSDelete: ignoring %s\n", key)
		return
	}
	ctx.deviceNetworkStatus = types.DeviceNetworkStatus{}
	newAddrCount := types.CountLocalAddrAnyNoLinkLocal(ctx.deviceNetworkStatus)
	log.Infof("handleDNSDelete %d usable addresses\n", newAddrCount)
	if (ctx.usableAddressCount == 0 && newAddrCount != 0) ||
		(ctx.usableAddressCount != 0 && newAddrCount == 0) {
		ctx.usableAddressCount = newAddrCount
		updateDerivedLedCounter(ctx)
	}
	log.Infof("handleDNSDelete done for %s\n", key)
}

func handleGlobalConfigModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*ledManagerContext)
	if key != "global" {
		log.Infof("handleGlobalConfigModify: ignoring %s\n", key)
		return
	}
	log.Infof("handleGlobalConfigModify for %s\n", key)
	debug, _ = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
		debugOverride)
	log.Infof("handleGlobalConfigModify done for %s\n", key)
}

func handleGlobalConfigDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*ledManagerContext)
	if key != "global" {
		log.Infof("handleGlobalConfigDelete: ignoring %s\n", key)
		return
	}
	log.Infof("handleGlobalConfigDelete for %s\n", key)
	debug, _ = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
		debugOverride)
	log.Infof("handleGlobalConfigDelete done for %s\n", key)
}
