// Copyright (c) 2017,2018 Zededa, Inc.
// All rights reserved.

// Look for address changes

package devicenetwork

import (
	"errors"
	"fmt"
	"github.com/eriknordmark/netlink"
	log "github.com/sirupsen/logrus"
	"github.com/zededa/go-provision/types"
	"net"
	"reflect"
)

// Returns a channel for address updates
// Caller then does this in select loop:
//	case change := <-addrChanges:
//		devicenetwork.AddrChange(&clientCtx, change)
//
func AddrChangeInit(ctx *DeviceNetworkContext) chan netlink.AddrUpdate {
	log.Debugf("AddrChangeInit()\n")
	IfindexToAddrsInit()

	addrchan := make(chan netlink.AddrUpdate)
	errFunc := func(err error) {
		log.Errorf("AddrSubscribe failed %s\n", err)
	}
	addropt := netlink.AddrSubscribeOptions{
		ListExisting:      true,
		ErrorCallback:     errFunc,
		ReceiveBufferSize: 128 * 1024,
	}
	if err := netlink.AddrSubscribeWithOptions(addrchan, nil,
		addropt); err != nil {
		log.Fatal(err)
	}
	return addrchan
}

// Handle an IP address change
func AddrChange(ctx *DeviceNetworkContext, change netlink.AddrUpdate) {

	changed := false
	if change.NewAddr {
		changed = IfindexToAddrsAdd(ctx, change.LinkIndex,
			change.LinkAddress)
	} else {
		changed = IfindexToAddrsDel(ctx, change.LinkIndex,
			change.LinkAddress)
	}
	if changed {
		HandleAddressChange(ctx, "any")
	}
}

// Check if ports in the given DeviceNetworkStatus have atleast one
// IP address each.
func checkIfAllDNSPortsHaveIPAddrs(status types.DeviceNetworkStatus) bool {
	mgmtPorts := types.GetMgmtPortsFree(status, 0)
	if len(mgmtPorts) == 0 {
		return false
	}

	for _, port := range mgmtPorts {
		numAddrs := types.CountLocalAddrFreeNoLinkLocalIf(status, port)
		log.Debugln("checkIfAllDNSPortsHaveIPAddrs: Port %s has %d addresses.",
			port, numAddrs)
		if numAddrs < 1 {
			return false
		}
	}
	return true
}

// The ifname arg can only be used for logging
func HandleAddressChange(ctx *DeviceNetworkContext,
	ifname string) {

	// Check if we have more or less addresses
	var dnStatus types.DeviceNetworkStatus

	// XXX if err return means WPAD failed, or port does not exist
	// XXX add test hook for former
	if !ctx.Pending.Inprogress {
		dnStatus = *ctx.DeviceNetworkStatus
		status, _ := MakeDeviceNetworkStatus(*ctx.DevicePortConfig,
			dnStatus)

		if !reflect.DeepEqual(*ctx.DeviceNetworkStatus, status) {
			log.Debugf("HandleAddressChange: change for %s from %v to %v\n",
				ifname, *ctx.DeviceNetworkStatus, status)
			*ctx.DeviceNetworkStatus = status
			DoDNSUpdate(ctx)
		} else {
			log.Infof("HandleAddressChange: No change for %s\n", ifname)
		}
	} else {
		dnStatus = ctx.Pending.PendDNS
		_, _ = MakeDeviceNetworkStatus(*ctx.DevicePortConfig, dnStatus)

		pingTestDNS := checkIfAllDNSPortsHaveIPAddrs(dnStatus)
		if pingTestDNS {
			// We have a suitable candiate for running our cloud ping test.
			// Kick the DNS test timer to fire immediately.
			log.Infof("HandleAddressChange: Kicking cloud ping test now, " +
				"Since we have suitable addresses already.")
			VerifyDevicePortConfig(ctx)
		}
	}
}

// ===== map from ifindex to list of IP addresses

var ifindexToAddrs map[int][]net.IPNet

func IfindexToAddrsInit() {
	ifindexToAddrs = make(map[int][]net.IPNet)
}

// Returns true if added
func IfindexToAddrsAdd(ctx *DeviceNetworkContext, index int, addr net.IPNet) bool {
	addrs, ok := ifindexToAddrs[index]
	if !ok {
		log.Debugf("IfindexToAddrsAdd add %v for %d\n", addr, index)
		ifindexToAddrs[index] = append(ifindexToAddrs[index], addr)
		// log.Debugf("ifindexToAddrs post add %v\n", ifindexToAddrs)
		return true
	}
	found := false
	for _, a := range addrs {
		// Equal if containment in both directions?
		if a.IP.Equal(addr.IP) &&
			a.Contains(addr.IP) && addr.Contains(a.IP) {
			found = true
			break
		}
	}
	if !found {
		log.Debugf("IfindexToAddrsAdd add %v for %d\n", addr, index)
		ifindexToAddrs[index] = append(ifindexToAddrs[index], addr)
		// log.Debugf("ifindexToAddrs post add %v\n", ifindexToAddrs)
	}
	return !found
}

// Returns true if deleted
func IfindexToAddrsDel(ctx *DeviceNetworkContext, index int, addr net.IPNet) bool {
	addrs, ok := ifindexToAddrs[index]
	if !ok {
		log.Warnf("IfindexToAddrsDel unknown index %d\n", index)
		return false
	}
	for i, a := range addrs {
		// Equal if containment in both directions?
		if a.IP.Equal(addr.IP) &&
			a.Contains(addr.IP) && addr.Contains(a.IP) {
			log.Debugf("IfindexToAddrsDel del %v for %d\n",
				addr, index)
			ifindexToAddrs[index] = append(ifindexToAddrs[index][:i],
				ifindexToAddrs[index][i+1:]...)
			// log.Debugf("ifindexToAddrs post remove %v\n", ifindexToAddrs)
			// XXX should we check for zero and remove ifindex?
			return true
		}
	}
	log.Warnf("IfindexToAddrsDel address not found for %d in\n",
		index, addrs)
	return false
}

func IfindexToAddrs(index int) ([]net.IPNet, error) {
	addrs, ok := ifindexToAddrs[index]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Unknown ifindex %d", index))
	}
	return addrs, nil
}
