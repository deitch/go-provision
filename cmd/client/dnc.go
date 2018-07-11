// Copyright (c) 2017-2018 Zededa, Inc.
// All rights reserved.

package client

import (
	"github.com/zededa/go-provision/cast"
	"github.com/zededa/go-provision/devicenetwork"
	"github.com/zededa/go-provision/types"
	"log"
	"reflect"
)

func handleDNCModify(ctxArg interface{}, configFilename string,
	configArg interface{}) {
	config := cast.CastDeviceNetworkConfig(configArg)
	ctx := ctxArg.(*clientContext)

	if configFilename != ctx.manufacturerModel {
		if debug {
			log.Printf("handleDNCModify: ignoring %s - expecting %s\n",
				configFilename, ctx.manufacturerModel)
		}
		return
	}
	log.Printf("handleDNCModify for %s\n", configFilename)

	ctx.deviceNetworkConfig = config
	new, _ := devicenetwork.MakeDeviceNetworkStatus(config,
		ctx.deviceNetworkStatus)
	// XXX switch to Equal?
	if !reflect.DeepEqual(ctx.deviceNetworkStatus, new) {
		log.Printf("DeviceNetworkStatus change from %v to %v\n",
			ctx.deviceNetworkStatus, new)
		ctx.deviceNetworkStatus = new
		doDNSUpdate(ctx)
	}
	log.Printf("handleDNCModify done for %s\n", configFilename)
}

func handleDNCDelete(ctxArg interface{}, configFilename string) {
	log.Printf("handleDNCDelete for %s\n", configFilename)
	ctx := ctxArg.(*clientContext)

	if configFilename != "global" {
		log.Printf("handleDNSDelete: ignoring %s\n", configFilename)
		return
	}
	new := types.DeviceNetworkStatus{}
	// XXX switch to Equal?
	if !reflect.DeepEqual(ctx.deviceNetworkStatus, new) {
		log.Printf("DeviceNetworkStatus change from %v to %v\n",
			ctx.deviceNetworkStatus, new)
		ctx.deviceNetworkStatus = new
		doDNSUpdate(ctx)
	}
	log.Printf("handleDNCDelete done for %s\n", configFilename)
}

func doDNSUpdate(ctx *clientContext) {
	// Did we loose all usable addresses or gain the first usable
	// address?
	newAddrCount := types.CountLocalAddrAnyNoLinkLocal(ctx.deviceNetworkStatus)
	if newAddrCount == 0 && ctx.usableAddressCount != 0 {
		log.Printf("DeviceNetworkStatus from %d to %d addresses\n",
			newAddrCount, ctx.usableAddressCount)
		// Inform ledmanager that we have no addresses
		types.UpdateLedManagerConfig(1)
	} else if newAddrCount != 0 && ctx.usableAddressCount == 0 {
		log.Printf("DeviceNetworkStatus from %d to %d addresses\n",
			newAddrCount, ctx.usableAddressCount)
		// Inform ledmanager that we have uplink addresses
		types.UpdateLedManagerConfig(2)
	}
	ctx.usableAddressCount = newAddrCount
	// XXX need general callback to use this function in zedrouter
}