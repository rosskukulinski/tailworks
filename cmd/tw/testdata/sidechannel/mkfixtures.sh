#!/bin/sh
# mkfixtures.sh: (re)writes the sysfs/proc fixture trees next to it for
# cmd/tw.TestDoctorSideChannelsFixtures. Each tree is what a Linux kernel
# exposes under /sys/devices/system/cpu/{vulnerabilities,smt},
# /sys/kernel/mm/ksm and /proc/cmdline, for one posture.
set -eu
cd "$(dirname "$0")"

# tree <name> <smt active|-> <cmdline> <ksm run|-> [extra vulnerability status]
tree() {
	d=$1
	rm -rf "$d"
	mkdir -p "$d/sys/devices/system/cpu/vulnerabilities" "$d/proc"
	for v in itlb_multihit l1tf mds meltdown spectre_v1 spectre_v2 spec_store_bypass srbds tsx_async_abort; do
		case $v in
		meltdown) s="Mitigation: PTI" ;;
		spectre_v1) s="Mitigation: usercopy/swapgs barriers and __user pointer sanitization" ;;
		spectre_v2) s="Mitigation: Retpolines; IBPB: conditional; STIBP: disabled; RSB filling" ;;
		spec_store_bypass) s="Mitigation: Speculative Store Bypass disabled via prctl" ;;
		*) s="Not affected" ;;
		esac
		echo "$s" >"$d/sys/devices/system/cpu/vulnerabilities/$v"
	done
	if [ "${5:-}" ]; then
		echo "$5" >"$d/sys/devices/system/cpu/vulnerabilities/spec_rstack_overflow"
	fi
	if [ "$2" != - ]; then
		mkdir -p "$d/sys/devices/system/cpu/smt"
		echo "$2" >"$d/sys/devices/system/cpu/smt/active"
	fi
	echo "$3" >"$d/proc/cmdline"
	if [ "$4" != - ]; then
		mkdir -p "$d/sys/kernel/mm/ksm"
		echo "$4" >"$d/sys/kernel/mm/ksm/run"
	fi
}

tree mitigated 0 "BOOT_IMAGE=/vmlinuz root=UUID=x ro nosmt console=ttyS0" 0
tree vulnerable 0 "BOOT_IMAGE=/vmlinuz root=UUID=x ro nosmt" 0 "Vulnerable: Safe RET, no microcode"
tree smt_on 1 "BOOT_IMAGE=/vmlinuz root=UUID=x ro" 0
tree mitigations_off 0 "BOOT_IMAGE=/vmlinuz root=UUID=x ro mitigations=off nosmt" 0
tree ksm_on 0 "BOOT_IMAGE=/vmlinuz root=UUID=x ro nosmt" 1

# A kernel before 4.15: no vulnerabilities directory, no smt, no ksm.
rm -rf old_kernel
mkdir -p old_kernel/proc old_kernel/sys/devices/system/cpu
echo "BOOT_IMAGE=/vmlinuz root=UUID=x ro" >old_kernel/proc/cmdline
: >old_kernel/sys/devices/system/cpu/.keep
