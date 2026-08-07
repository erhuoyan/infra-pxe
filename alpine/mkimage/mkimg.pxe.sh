#!/bin/sh
# mkimg.pxe.sh — mkimage profile for infra-pxe engine ISO
#
# Extends profile_standard.  Apk packages baked into ISO.
# Custom section_payload injects infra-pxe payload to ISO root.

profile_pxe() {
	profile_standard
	profile_abbrev="pxe"
	title="Infra PXE Engine"
	desc="Infra-pxe Engine Live ISO. 启动后 pxe-net 配网即用."
	arch="x86_64 aarch64"

	# Packages — baked into ISO apks, no network needed at boot
	apks="$apks
		dnsmasq ipmitool sqlite bash jq curl
		iproute2 bonding util-linux openssh
		e2fsprogs dosfstools sfdisk parted
		"

	# Use custom overlay generator
	apkovl="genapkovl-pxe.sh"
	hostname="pxe-engine"
}

# ── build_payload: inject infra-pxe.tar.gz into ISO root ──
# Called by mkimage.sh during ISO build. Puts payload files at ISO root.
# At boot, files accessible via /media/cdrom/

build_payload() {
	local _payload="${PXE_PAYLOAD_DIR:-/home/build/payload}"
	if [ -d "$_payload" ] && [ "$(ls -A "$_payload" 2>/dev/null)" ]; then
		cp "$_payload"/* "$DESTDIR"/
		msg "pxe payload injected"
	fi
}

# ── section_payload: checksum-based rebuild ──
section_payload() {
	local _payload="${PXE_PAYLOAD_DIR:-/home/build/payload}"
	if [ -d "$_payload" ] && [ "$(ls -A "$_payload" 2>/dev/null)" ]; then
		local _id=$(ls -la "$_payload" | sha256sum | cut -d' ' -f1)
		build_section payload "$_id"
	fi
}
