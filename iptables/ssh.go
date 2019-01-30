// Copyright (c) 2018 Zededa, Inc.
// All rights reserved.

// Also blocks the VNC ports (5900...) if ssh is blocked
// Always blocks 4822
// Also always blocks port 8080

package iptables

import (
	"fmt"
	log "github.com/sirupsen/logrus"
)

func UpdateSshAccess(enable bool, first bool) {

	log.Infof("updateSshAccess(enable %v first %v)\n",
		enable, first)

	if first {
		// Always blocked
		dropPortRange(8080, 8080)
		dropPortRange(4822, 4822)
	}
	if enable {
		allowPortRange(22, 22)
	} else {
		dropPortRange(22, 22)
	}
}

func UpdateVncAccess(enable bool) {

	log.Infof("updateVncAccess(enable %v\n", enable)

	if enable {
		allowPortRange(5900, 5999)
	} else {
		dropPortRange(5900, 5999)
	}
}

func allowPortRange(startPort int, endPort int) {
	log.Infof("allowPortRange(%d, %d)\n", startPort, endPort)
	// Delete these rules
	// iptables -D INPUT -p tcp --dport 22 -j REJECT --reject-with tcp-reset
	// ip6tables -D INPUT -p tcp --dport 22 -j REJECT --reject-with tcp-reset
	var portStr string
	if startPort == endPort {
		portStr = fmt.Sprintf("%d", startPort)
	} else {
		portStr = fmt.Sprintf("%d:%d", startPort, endPort)
	}
	IptableCmd("-D", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "REJECT", "--reject-with", "tcp-reset")
	Ip6tableCmd("-D", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "REJECT", "--reject-with", "tcp-reset")
}

func dropPortRange(startPort int, endPort int) {
	log.Infof("dropPortRange(%d, %d)\n", startPort, endPort)
	// Add these rules
	// iptables -A INPUT -p tcp --dport 22 -j REJECT --reject-with tcp-reset
	// ip6tables -A INPUT -p tcp --dport 22 -j REJECT --reject-with tcp-reset
	var portStr string
	if startPort == endPort {
		portStr = fmt.Sprintf("%d", startPort)
	} else {
		portStr = fmt.Sprintf("%d:%d", startPort, endPort)
	}
	IptableCmd("-A", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "REJECT", "--reject-with", "tcp-reset")
	Ip6tableCmd("-A", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "REJECT", "--reject-with", "tcp-reset")
}
