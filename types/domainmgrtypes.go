// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

package types

import (
	log "github.com/sirupsen/logrus"
	"time"
)

// The information XenManager needs to boot and halt domains
// If the the version (in UUIDandVersion) changes then the domain needs to
// halted and booted?? NO, because an ACL change from ZedControl would bump
// the version. Who determines which changes require halt+reboot?
// Do we need an explicit interlock with ZedManager when a reboot
// is needed? For instance, ZedManager could remove the DomainConfig, what for
// DomainStatus to be deleted, then re-create the DomainConfig.
type DomainConfig struct {
	UUIDandVersion UUIDandVersion
	DisplayName    string // Use as name for domU? DisplayName+version?
	Activate       bool   // Actually start the domU as opposed to prepare
	AppNum         int    // From networking; makes the name unique
	VmConfig
	DiskConfigList    []DiskConfig
	VifList           []VifInfo
	IoAdapterList     []IoAdapter
	CloudInitUserData string // base64-encoded
}

func (config DomainConfig) Key() string {
	return config.UUIDandVersion.UUID.String()
}

func (config DomainConfig) VerifyFilename(fileName string) bool {
	expect := config.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained uuid: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

// Some of these items can be overridden by matching Targets in
// StorageConfigList. For example, a Target of "kernel" means to set/override
// the Kernel attribute below.
type VmConfig struct {
	Kernel     string // default ""
	Ramdisk    string // default ""
	Memory     int    // in kbytes; Rounded up to Mbytes for xen
	MaxMem     int    // Default not set i.e. no ballooning
	VCpus      int    // default 1
	MaxCpus    int    // default VCpus
	RootDev    string // default "/dev/xvda1"
	ExtraArgs  string // added to bootargs
	BootLoader string // default ""
	// For CPU pinning
	CPUs string // default "", list of "1,2"
	// Needed for device passthru
	DeviceTree string // default ""; sets device_tree
	// Example: device_tree="guest-gpio.dtb"
	DtDev []string // default nil; sets dtdev
	// Example, DtDev=["/smb/gpio@f7020000","/smb/gpio@f8013000"]
	IRQs []int // default nil; sets irqs
	// Example, IRQs=[88,86]
	IOMem []string // default nil; sets iomem
	// Example, IOMem=["0xf7020,1","0xf8013,1"]
	VirtualizationMode VmMode
	EnableVnc          bool
	VncDisplay         uint32
	VncPasswd          string
}

type VmMode uint8

const (
	PV VmMode = iota + 0 // Default
	HVM
	// PVH
)

type DomainStatus struct {
	UUIDandVersion     UUIDandVersion
	DisplayName        string
	State              SwState // BOOTING and above?
	Activated          bool    // XXX remove??
	AppNum             int
	PendingAdd         bool
	PendingModify      bool
	PendingDelete      bool
	DomainName         string // Name of Xen domain
	DomainId           int
	BootTime           time.Time
	DiskStatusList     []DiskStatus
	VifList            []VifInfo
	IoAdapterList      []IoAdapter
	VirtualizationMode VmMode
	EnableVnc          bool
	VncDisplay         uint32
	VncPasswd          string
	TriedCount         int
	LastErr            string // Xen error
	LastErrTime        time.Time
}

func (status DomainStatus) Key() string {
	return status.UUIDandVersion.UUID.String()
}

func (status DomainStatus) VerifyFilename(fileName string) bool {
	expect := status.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained uuid: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

func (status DomainStatus) CheckPendingAdd() bool {
	return status.PendingAdd
}

func (status DomainStatus) CheckPendingModify() bool {
	return status.PendingModify
}

func (status DomainStatus) CheckPendingDelete() bool {
	return status.PendingDelete
}

func (status DomainStatus) Pending() bool {
	return status.PendingAdd || status.PendingModify || status.PendingDelete
}

type VifInfo struct {
	Bridge string
	Vif    string
	Mac    string
}

// XenManager will pass these to the xen xl config file
// The vdev is automatically assigned as xvd[x], where X is a, b, c etc,
// based on the order in the DiskList
// Note that vdev in general can be hd[x], xvd[x], sd[x] but here we only
// use xvd
type DiskConfig struct {
	ImageSha256 string // sha256 of immutable image
	ReadOnly    bool
	Preserve    bool // If set a rw disk will be preserved across
	// boots (acivate/inactivate)
	Maxsizebytes uint64 // Resize filesystem to this size if set
	Format       string // Default "raw"; could be raw, qcow, qcow2, vhd
	Devtype      string // Default ""; could be e.g. "cdrom"
}

type DiskStatus struct {
	ImageSha256        string // sha256 of immutable image
	ReadOnly           bool
	Preserve           bool
	FileLocation       string // Local location of Image
	Maxsizebytes       uint64 // Resize filesystem to this size if set
	Format             string // From config
	Devtype            string // From config
	Vdev               string // Allocated
	ActiveFileLocation string // Allocated; private copy if RW; FileLocation if RO
}

// Track the active image files in rwImgDirname
type ImageStatus struct {
	Filename     string // Basename; used as key
	FileLocation string // Local location of Image
	RefCount     uint
	LastUse      time.Time // When RefCount dropped to zero
	Size         uint64
}

func (status ImageStatus) Key() string {
	return status.Filename
}
