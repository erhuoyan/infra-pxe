#!/usr/bin/env python3
"""Hardware inventory collector for PXE Station.

Standalone script — can be run on any Linux machine to collect hardware info.
Outputs JSON to stdout. Requires root for full info (dmidecode, smartctl).

Usage:
    # Collect and print JSON (debug mode)
    python3 hw-collect.py

    # Collect and report to PXE Station API
    python3 hw-collect.py --report http://pxe-server:8000

    # Pretty-print for debugging
    python3 hw-collect.py --pretty

    # Only collect specific categories
    python3 hw-collect.py --only cpus,memory,disks
"""

import argparse
import glob
import json
import os
import re
import subprocess
import sys


def run(cmd):
    """Run a shell command and return stdout, or empty string on failure."""
    try:
        r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=30)
        return r.stdout.strip() if r.returncode == 0 else ""
    except (subprocess.TimeoutExpired, Exception):
        return ""


def collect_system():
    """Collect system/board info from dmidecode."""
    info = {}
    try:
        bios_ver = run("dmidecode -s bios-version 2>/dev/null")
        bios_date = run("dmidecode -s bios-release-date 2>/dev/null")
        board_mfr = run("dmidecode -s baseboard-manufacturer 2>/dev/null")
        board_prod = run("dmidecode -s baseboard-product-name 2>/dev/null")
        sys_mfr = run("dmidecode -s system-manufacturer 2>/dev/null")
        sys_prod = run("dmidecode -s system-product-name 2>/dev/null")
        # BMC firmware from ipmitool or dmidecode type 38
        bmc_ver = run(
            "ipmitool mc info 2>/dev/null | grep 'Firmware Revision' | awk -F: '{print $2}'"
        ).strip()
        if not bmc_ver:
            bmc_ver = run(
                "dmidecode -t 38 2>/dev/null | grep -i 'firmware' | head -1 | sed 's/.*: *//'"
            )
        # Memory slot count
        mem_slots_raw = run(
            "dmidecode -t 16 2>/dev/null | grep 'Number Of Devices' | awk '{print $NF}'"
        )
        mem_slots = int(mem_slots_raw) if mem_slots_raw.isdigit() else 0
        # PCI device count
        pci_slots = int(run("lspci 2>/dev/null | wc -l") or "0")
        info = {
            "bios_version": f"{bios_ver} ({bios_date})" if bios_date else bios_ver,
            "bmc_version": bmc_ver,
            "board_manufacturer": board_mfr,
            "board_product": board_prod,
            "system_manufacturer": sys_mfr,
            "system_product": sys_prod,
            "memory_slot_count": mem_slots,
            "pci_device_count": pci_slots,
        }
    except Exception:
        pass
    return info


def collect_cpus():
    """Collect CPU info from lscpu + dmidecode."""
    cpus = []
    try:
        cpu_model = run("lscpu | grep 'Model name' | sed 's/.*: *//'").split("\n")[0].strip()
        cores_per = int(run("lscpu | grep 'Core(s) per socket' | awk '{print $NF}'") or "0")
        threads_per = int(run("lscpu | grep 'Thread(s) per core' | awk '{print $NF}'") or "0")
        sockets = int(run("lscpu | grep 'Socket(s)' | awk '{print $2}'") or "1")
        max_freq = run("lscpu | grep 'CPU max MHz' | awk '{print $NF}'")
        max_freq_ghz = round(float(max_freq) / 1000, 2) if max_freq else 0
        base_freq = run("lscpu | grep 'CPU MHz' | awk '{print $NF}'")
        if not base_freq:
            base_freq = run("lscpu | grep 'CPU min MHz' | awk '{print $NF}'")
        freq_ghz = round(float(base_freq) / 1000, 2) if base_freq else max_freq_ghz
        arch = run("uname -m")
        cpu_vendor = run("lscpu | grep 'Vendor ID' | sed 's/.*: *//'")
        vendor_map = {"GenuineIntel": "Intel", "AuthenticAMD": "AMD", "HiSilicon": "HiSilicon"}
        cpu_manufacturer = vendor_map.get(cpu_vendor, cpu_vendor)
        # Per-socket serial from dmidecode
        cpu_serials = []
        dmi_cpu = run("dmidecode -t processor 2>/dev/null")
        for block in dmi_cpu.split("Processor Information")[1:]:
            sn_m = re.search(r"Serial Number:\s+(.+)", block)
            cpu_serials.append(
                sn_m.group(1).strip() if sn_m and "Not Specified" not in sn_m.group(1) else ""
            )
        for i in range(sockets):
            cpus.append(
                {
                    "slot": f"CPU{i}",
                    "model": cpu_model,
                    "manufacturer": cpu_manufacturer,
                    "cores": cores_per,
                    "threads": cores_per * threads_per,
                    "freq_ghz": freq_ghz,
                    "max_freq_ghz": max_freq_ghz,
                    "arch": arch,
                    "serial_number": cpu_serials[i] if i < len(cpu_serials) else "",
                }
            )
    except Exception:
        pass
    return cpus


def collect_memory():
    """Collect memory DIMMs from dmidecode."""
    memory = []
    try:
        dmi = run("dmidecode -t memory 2>/dev/null")
        for block in dmi.split("Memory Device")[1:]:
            size_m = re.search(r"Size:\s+(\d+)\s*(MB|GB)", block)
            if not size_m:
                continue
            size_val = int(size_m.group(1))
            cap_gb = size_val if "GB" in size_m.group(2) else round(size_val / 1024, 1)
            slot = re.search(r"Locator:\s+(.+)", block)
            mtype = re.search(r"Type:\s+(\S+)", block)
            speed = re.search(r"Configured Memory Speed:\s+(\d+)", block) or re.search(
                r"Speed:\s+(\d+)", block
            )
            sn = re.search(r"Serial Number:\s+(\S+)", block)
            mfr = re.search(r"Manufacturer:\s+(.+)", block)
            part = re.search(r"Part Number:\s+(.+)", block)
            memory.append(
                {
                    "slot": slot.group(1).strip() if slot else "",
                    "capacity_gb": cap_gb,
                    "type": mtype.group(1) if mtype else "",
                    "speed_mhz": int(speed.group(1)) if speed else 0,
                    "ecc": 1 if "ECC" in block else 0,
                    "manufacturer": mfr.group(1).strip()
                    if mfr and "Not Specified" not in mfr.group(1)
                    else "",
                    "part_number": part.group(1).strip()
                    if part and "Not Specified" not in part.group(1)
                    else "",
                    "serial_number": sn.group(1) if sn and sn.group(1) != "Not Specified" else "",
                }
            )
    except Exception:
        pass
    return memory


def collect_disks():
    """Collect block devices from sysfs + smartctl."""
    disks = []
    try:
        for dev in (
            glob.glob("/sys/block/sd*")
            + glob.glob("/sys/block/nvme*")
            + glob.glob("/sys/block/vd*")
        ):
            name = os.path.basename(dev)
            if not os.path.exists(f"{dev}/size"):
                continue
            size_sectors = int(open(f"{dev}/size").read().strip())
            size_gb = round(size_sectors * 512 / (1024**3), 1)
            if size_gb < 1:
                continue
            model = run(f"cat {dev}/device/model 2>/dev/null").strip()
            serial = run(
                f"cat {dev}/device/serial 2>/dev/null || smartctl -i /dev/{name} 2>/dev/null | grep 'Serial' | awk '{{print $NF}}'"
            ).strip()
            firmware = run(
                f"smartctl -i /dev/{name} 2>/dev/null | grep -i 'Firmware' | awk '{{print $NF}}'"
            ).strip()
            # Manufacturer
            manufacturer = run(
                f"smartctl -i /dev/{name} 2>/dev/null | grep -i 'Vendor' | sed 's/.*: *//'"
            ).strip()
            if not manufacturer and model:
                for prefix in [
                    "Samsung",
                    "WDC",
                    "HGST",
                    "Seagate",
                    "Micron",
                    "Intel",
                    "KIOXIA",
                    "Toshiba",
                    "SK hynix",
                ]:
                    if prefix.lower() in model.lower():
                        manufacturer = prefix
                        break
            # Form factor
            ff_raw = run(
                f"smartctl -i /dev/{name} 2>/dev/null | grep -i 'Form Factor' | sed 's/.*: *//'"
            ).strip()
            form_factor = ff_raw if ff_raw else ""
            if "nvme" in name and not form_factor:
                form_factor = "M.2" if size_gb < 1500 else "E1.S"
            # Rotation rate
            rpm_raw = run(
                f"smartctl -i /dev/{name} 2>/dev/null | grep -i 'Rotation Rate' | sed 's/.*: *//'"
            ).strip()
            rpm = 0
            if rpm_raw and "Solid State" not in rpm_raw:
                rpm_digits = re.search(r"(\d+)", rpm_raw)
                rpm = int(rpm_digits.group(1)) if rpm_digits else 0
            # NAND type
            nand_type = ""
            smart_all = run(f"smartctl -a /dev/{name} 2>/dev/null")
            if smart_all:
                nand_m = re.search(r"(TLC|MLC|QLC|SLC)", smart_all, re.IGNORECASE)
                if nand_m:
                    nand_type = nand_m.group(1).upper()
            # Interface detection
            rotational = ""
            if "nvme" in name:
                interface = "NVMe"
            else:
                rotational = run(f"cat {dev}/queue/rotational 2>/dev/null").strip()
                if rotational == "0":
                    interface = "SSD"
                elif rotational == "1" and rpm > 0:
                    interface = "HDD"
                else:
                    # virtio/unknown: rotational=1 but no RPM info → use SATA as generic
                    interface = "SATA"
            transport = run(
                f"smartctl -i /dev/{name} 2>/dev/null | grep -i 'Transport' | sed 's/.*: *//'"
            ).strip()
            if transport and "SAS" in transport and interface not in ("NVMe",):
                interface = "SAS SSD" if rpm == 0 else "SAS HDD"
            elif transport and "SATA" in transport and interface == "SSD":
                interface = "SATA SSD"
            if "NVMe" in interface and form_factor:
                interface = f"NVMe-{form_factor}"
            # Media: SSD if NVMe, or no rotation, or interface says SSD
            media = (
                "SSD"
                if ("nvme" in name or "SSD" in interface or (rpm == 0 and rotational == "0"))
                else "HDD"
                if rpm > 0
                else ""
            )
            # Purpose
            purpose = ""
            dev_path = f"/dev/{name}"
            mounts = run("findmnt -n -o SOURCE / 2>/dev/null")
            if mounts and name in mounts:
                purpose = "sys"
            disks.append(
                {
                    "slot": name,
                    "capacity_gb": size_gb,
                    "interface": interface
                    if any(x in interface for x in ("NVMe", "NVME", "SSD", "HDD", "SAS"))
                    else "SATA",
                    "media": media,
                    "model": model,
                    "manufacturer": manufacturer,
                    "form_factor": form_factor,
                    "rpm": rpm,
                    "nand_type": nand_type,
                    "purpose": purpose,
                    "dev_path": dev_path,
                    "serial_number": serial,
                    "firmware": firmware,
                }
            )
        # Mark non-sys disks as "data"
        if any(d["purpose"] == "sys" for d in disks):
            for d in disks:
                if not d["purpose"]:
                    d["purpose"] = "data"
    except Exception:
        pass
    return disks


def collect_nics_and_ports():
    """Collect NIC cards and network ports."""
    nics = []
    ports = []

    # Build PCI address → lspci info map
    pci_info = {}
    try:
        lspci_out = run("lspci -nn 2>/dev/null")
        for line in lspci_out.split("\n"):
            if not line.strip():
                continue
            addr = line.split(" ", 1)[0]
            desc = line.split(": ", 1)[-1] if ": " in line else line
            pci_info[addr] = desc
    except Exception:
        pass

    # Build PCI base → interfaces map
    nic_pci_base = {}
    try:
        for iface in os.listdir("/sys/class/net/"):
            if iface in ("lo",) or iface.startswith(("bond", "vlan", "br", "docker", "virbr")):
                continue
            try:
                pci_addr = os.path.basename(os.readlink(f"/sys/class/net/{iface}/device"))
            except Exception:
                continue
            if ":" not in pci_addr:
                continue
            base = pci_addr.rsplit(".", 1)[0]
            nic_pci_base.setdefault(base, []).append(iface)
    except Exception:
        pass

    # Collect per-NIC card + per-port
    seen_nic_bases = set()
    try:
        for iface in sorted(os.listdir("/sys/class/net/")):
            if iface in ("lo",) or iface.startswith(("bond", "vlan", "br", "docker", "virbr")):
                continue
            if not os.path.exists(f"/sys/class/net/{iface}/device"):
                continue
            mac = open(f"/sys/class/net/{iface}/address").read().strip().upper()
            if mac == "00:00:00:00:00:00":
                continue
            pci_addr = ""
            try:
                pci_addr = os.path.basename(os.readlink(f"/sys/class/net/{iface}/device"))
            except Exception:
                pass
            base = pci_addr.rsplit(".", 1)[0] if pci_addr and ":" in pci_addr else iface
            slot = pci_addr if pci_addr and ":" in pci_addr else iface

            # One NIC entry per physical card (PCI base)
            if base not in seen_nic_bases:
                seen_nic_bases.add(base)
                speed_raw = run(f"cat /sys/class/net/{iface}/speed 2>/dev/null")
                speed_gbps = round(int(speed_raw) / 1000, 1) if speed_raw.isdigit() else 0
                driver = (
                    os.path.basename(os.readlink(f"/sys/class/net/{iface}/device/driver"))
                    if os.path.exists(f"/sys/class/net/{iface}/device/driver")
                    else ""
                )
                short_addr = (
                    pci_addr.replace("0000:", "") if pci_addr.startswith("0000:") else pci_addr
                )
                model = pci_info.get(short_addr, "")
                manufacturer = ""
                if model:
                    for mfr in [
                        "Intel",
                        "Mellanox",
                        "Broadcom",
                        "Realtek",
                        "Huawei",
                        "HiSilicon",
                        "Marvell",
                        "Chelsio",
                        "Cavium",
                        "Netronome",
                        "NVIDIA",
                    ]:
                        if mfr.lower() in model.lower():
                            manufacturer = mfr
                            break
                interface_type = "PCIE"
                port_type = ""
                et_out = run(f"ethtool {iface} 2>/dev/null")
                if et_out:
                    pt_m = re.search(r"Supported ports:\s*\[\s*(.+?)\s*\]", et_out)
                    if pt_m:
                        port_type = pt_m.group(1).strip()
                    if not port_type:
                        pt_m2 = re.search(r"Port:\s+(.+)", et_out)
                        if pt_m2:
                            port_type = pt_m2.group(1).strip()
                    trans_m = re.search(r"Transceiver.*?type.*?:\s*(.+)", et_out, re.IGNORECASE)
                    if trans_m:
                        port_type = trans_m.group(1).strip()
                module_info = run(f"ethtool -m {iface} 2>/dev/null | head -5")
                if module_info and not port_type:
                    if "QSFP" in module_info:
                        port_type = "QSFP"
                    elif "SFP" in module_info:
                        port_type = "SFP+"
                fw = run(
                    f"ethtool -i {iface} 2>/dev/null | grep 'firmware-version' | sed 's/.*: *//'"
                ).strip()
                port_count = len(nic_pci_base.get(base, [iface]))
                nics.append(
                    {
                        "slot": slot,
                        "mac": mac,
                        "speed_gbps": speed_gbps,
                        "model": model,
                        "manufacturer": manufacturer,
                        "interface_type": interface_type,
                        "port_type": port_type,
                        "port_count": port_count,
                        "driver": driver,
                        "firmware": fw,
                    }
                )

            # Always record the port
            speed_raw = run(f"cat /sys/class/net/{iface}/speed 2>/dev/null")
            speed_gbps = round(int(speed_raw) / 1000, 1) if speed_raw.isdigit() else 0
            driver = (
                os.path.basename(os.readlink(f"/sys/class/net/{iface}/device/driver"))
                if os.path.exists(f"/sys/class/net/{iface}/device/driver")
                else ""
            )
            link_state = (
                open(f"/sys/class/net/{iface}/operstate").read().strip()
                if os.path.exists(f"/sys/class/net/{iface}/operstate")
                else ""
            )
            ip_addr = ""
            ip_out = run(f"ip -4 addr show {iface} 2>/dev/null | grep inet")
            if ip_out:
                ip_m = re.search(r"inet (\S+)", ip_out)
                ip_addr = ip_m.group(1) if ip_m else ""
            bond_name = ""
            bond_master = run(
                f"cat /sys/class/net/{iface}/master/uevent 2>/dev/null | grep INTERFACE | sed 's/INTERFACE=//'"
            ).strip()
            if bond_master:
                bond_name = bond_master
            purpose = ""
            module_sn = ""
            module_vendor = ""
            module_model = ""
            mod_out = run(f"ethtool -m {iface} 2>/dev/null")
            if mod_out:
                sn_m = re.search(r"Vendor SN\s*:\s*(\S+)", mod_out)
                vn_m = re.search(r"Vendor name\s*:\s*(.+)", mod_out)
                pn_m = re.search(r"Vendor PN\s*:\s*(\S+)", mod_out)
                module_sn = sn_m.group(1).strip() if sn_m else ""
                module_vendor = vn_m.group(1).strip() if vn_m else ""
                module_model = pn_m.group(1).strip() if pn_m else ""
            ports.append(
                {
                    "name": iface,
                    "mac": mac,
                    "pci_address": pci_addr,
                    "nic_slot": slot,
                    "speed_gbps": speed_gbps,
                    "link_state": link_state,
                    "ip": ip_addr,
                    "purpose": purpose,
                    "bond": bond_name,
                    "driver": driver,
                    "module_sn": module_sn,
                    "module_vendor": module_vendor,
                    "module_model": module_model,
                }
            )
    except Exception:
        pass

    return nics, ports


def collect_gpus():
    """Collect GPU info from lspci + nvidia-smi/rocm-smi."""
    gpus = []
    try:
        gpu_lines = run("lspci | grep -iE 'vga|3d|display|nvidia|amd'").split("\n")
        gpu_lines = [l for l in gpu_lines if l.strip()]
        if not gpu_lines:
            return gpus

        # nvidia-smi keyed by PCI bus_id
        nv_info = {}
        nv_csv = run(
            "nvidia-smi --query-gpu=gpu_bus_id,gpu_name,memory.total,serial,driver_version,vbios_version --format=csv,noheader,nounits 2>/dev/null"
        )
        if nv_csv:
            for line in nv_csv.strip().split("\n"):
                parts = [p.strip() for p in line.split(",")]
                if len(parts) >= 6:
                    bus_id = parts[0].lower()
                    if len(bus_id) > 12:
                        bus_id = bus_id.split(":", 1)[-1] if bus_id.count(":") > 2 else bus_id
                    short_bus = bus_id.lstrip("0").lstrip(":") if ":" in bus_id else bus_id
                    entry = {
                        "name": parts[1],
                        "vram_mb": int(parts[2]) if parts[2].isdigit() else 0,
                        "serial": parts[3] if parts[3] not in ("[N/A]", "N/A", "") else "",
                        "driver": parts[4],
                        "firmware": parts[5],
                    }
                    nv_info[bus_id] = entry
                    nv_info[short_bus] = entry

        # rocm-smi for AMD
        amd_info = {}
        rocm_out = run(
            "rocm-smi --showproductname --showserial --showvram --showdriverversion --csv 2>/dev/null"
        )
        if rocm_out:
            for line in rocm_out.strip().split("\n")[1:]:
                parts = [p.strip() for p in line.split(",")]
                if len(parts) >= 2:
                    amd_info[parts[0]] = {"name": parts[1]}

        amd_idx = 0
        for line in gpu_lines:
            model_str = line.split(": ", 1)[-1] if ": " in line else line
            slot = line.split(" ", 1)[0]
            manufacturer = ""
            vram = 0
            serial = ""
            driver = ""
            firmware = ""
            arch = ""
            interface_type = "PCIE"

            model_lower = model_str.lower()
            if "nvidia" in model_lower:
                manufacturer = "NVIDIA"
            elif "amd" in model_lower or "radeon" in model_lower or "navi" in model_lower:
                manufacturer = "AMD"
            elif "intel" in model_lower:
                manufacturer = "Intel"
            elif "huawei" in model_lower or "ascend" in model_lower:
                manufacturer = "Huawei"

            # Skip integrated graphics
            if any(
                x in model_lower
                for x in ("uhd graphics", "integrated", "hd graphics", "display controller [8086")
            ):
                continue

            # NVIDIA: match by PCI address
            slot_lower = slot.lower()
            if manufacturer == "NVIDIA":
                nv = nv_info.get(slot_lower) or nv_info.get(slot_lower.lstrip("0"), {})
                if nv:
                    model_str = nv.get("name") or model_str
                    vram = round(nv["vram_mb"] / 1024, 1) if nv.get("vram_mb") else 0
                    serial = nv.get("serial", "")
                    driver = nv.get("driver", "")
                    firmware = nv.get("firmware", "")
            elif manufacturer == "AMD":
                amd = amd_info.get(str(amd_idx), {})
                if amd.get("name"):
                    model_str = amd["name"]
                amd_idx += 1
                vram_path = f"/sys/bus/pci/devices/0000:{slot}/mem_info_vram_total"
                vram_bytes = run(f"cat {vram_path} 2>/dev/null")
                if vram_bytes.isdigit():
                    vram = round(int(vram_bytes) / (1024**3), 1)

            # Architecture detection
            nv_arch_map = {
                "H100": "Hopper",
                "H200": "Hopper",
                "H800": "Hopper",
                "A100": "Ampere",
                "A800": "Ampere",
                "A30": "Ampere",
                "A40": "Ampere",
                "V100": "Volta",
                "L40": "Ada Lovelace",
                "L4": "Ada Lovelace",
                "B100": "Blackwell",
                "B200": "Blackwell",
                "B300": "Blackwell",
                "GB200": "Blackwell",
                "GB300": "Blackwell",
                "G12": "Blackwell",
                "RTX 4090": "Ada Lovelace",
                "RTX 4080": "Ada Lovelace",
                "RTX 3090": "Ampere",
                "RTX 3080": "Ampere",
            }
            amd_arch_map = {
                "MI300": "CDNA 3",
                "MI250": "CDNA 2",
                "MI210": "CDNA 2",
                "MI100": "CDNA",
                "MI50": "Vega",
                "Instinct": "CDNA",
            }
            if manufacturer == "NVIDIA":
                for key, val in nv_arch_map.items():
                    if key.lower() in model_str.lower():
                        arch = val
                        break
            elif manufacturer == "AMD":
                for key, val in amd_arch_map.items():
                    if key.lower() in model_str.lower():
                        arch = val
                        break

            # Interface type
            if any(x in model_str.lower() for x in ("oam", "sxm")):
                interface_type = "OAM"
            elif "nvlink" in model_str.lower():
                interface_type = "SXM"

            gpus.append(
                {
                    "slot": slot,
                    "model": model_str,
                    "manufacturer": manufacturer,
                    "vram_gb": vram,
                    "interface_type": interface_type,
                    "arch": arch,
                    "serial_number": serial,
                    "driver": driver,
                    "firmware": firmware,
                }
            )
    except Exception:
        pass
    return gpus


def collect_psus():
    """Collect PSU info from dmidecode type 39."""
    psus = []
    try:
        dmi_psu = run("dmidecode -t 39 2>/dev/null")
        if dmi_psu:
            for i, block in enumerate(dmi_psu.split("System Power Supply")[1:]):
                name = re.search(r"Name:\s+(.+)", block)
                model = re.search(r"Model Part Number:\s+(.+)", block)
                sn = re.search(r"Serial Number:\s+(.+)", block)
                watt_m = re.search(r"Max Power Capacity:\s+(\d+)", block)
                rev = re.search(r"Revision:\s+(.+)", block)
                psus.append(
                    {
                        "slot": name.group(1).strip() if name else f"PSU{i}",
                        "model": model.group(1).strip()
                        if model and "Not Specified" not in model.group(1)
                        else "",
                        "watt": int(watt_m.group(1)) if watt_m else 0,
                        "serial_number": sn.group(1).strip()
                        if sn and "Not Specified" not in sn.group(1)
                        else "",
                        "firmware": rev.group(1).strip()
                        if rev and "Not Specified" not in rev.group(1)
                        else "",
                    }
                )
    except Exception:
        pass
    return psus


def generate_summary(components):
    """Generate hardware summary and config_summary string."""
    hw = {}
    hw["CPU"] = components["cpus"][0]["model"].split("\n")[0] if components.get("cpus") else ""
    hw["CORES"] = sum(x["cores"] for x in components.get("cpus", []))
    hw["memory_gb"] = sum(x.get("capacity_gb", 0) for x in components.get("memory", []))
    hw["DISKS"] = " | ".join(
        f"{d['slot']} {d.get('capacity_gb', 0)}GB {d.get('media', '')}"
        for d in components.get("disks", [])
    )
    hw["OS"] = ""
    try:
        with open("/etc/os-release") as f:
            for line in f:
                if line.startswith("PRETTY_NAME="):
                    hw["OS"] = line.split("=", 1)[1].strip().strip('"')
                    break
    except Exception:
        pass
    try:
        hw["KERNEL"] = open("/proc/version").read().split()[2]
    except Exception:
        hw["KERNEL"] = ""
    try:
        hw["hostname"] = open("/etc/hostname").read().strip()
    except Exception:
        hw["hostname"] = ""
    # System info
    sys_info = components.get("system", {})
    hw["bios_version"] = sys_info.get("bios_version", "")
    hw["bmc_version"] = sys_info.get("bmc_version", "")
    hw["board_manufacturer"] = sys_info.get("board_manufacturer", "")
    hw["board_product"] = sys_info.get("board_product", "")

    # Config summary: CPU*N/MEM*N/DISK_SUMMARY/GPU_SUMMARY/NIC_SUMMARY
    cpus = components.get("cpus", [])
    mems = components.get("memory", [])
    disks = components.get("disks", [])
    gpus = components.get("gpus", [])
    nics = components.get("nics", [])
    parts = []
    if cpus:
        cpu_short = cpus[0]["model"].split()[-1] if cpus[0].get("model") else "Unknown"
        parts.append(f"{cpu_short}*{len(cpus)}")
    if mems:
        mem_cap = int(mems[0].get("capacity_gb", 0))
        parts.append(f"{mem_cap}GB*{len(mems)}")
    if disks:
        dgrp = {}
        for d in disks:
            iface = d.get("interface", d.get("media", ""))
            key = f"{d.get('capacity_gb', 0):.0f}GB {iface}"
            dgrp[key] = dgrp.get(key, 0) + 1
        parts.append(" + ".join(f"{k}*{v}" for k, v in dgrp.items()))
    if gpus:
        ggrp = {}
        for g in gpus:
            vram = int(g.get("vram_gb", 0))
            key = f"{g.get('model', 'GPU')} {vram}GB" if vram else g.get("model", "GPU")
            ggrp[key] = ggrp.get(key, 0) + 1
        parts.append(" + ".join(f"{k}*{v}" for k, v in ggrp.items()))
    if nics:
        ngrp = {}
        for n in nics:
            sp = n.get("speed_gbps", 0)
            key = f"{sp:.0f}G" if sp >= 1 else f"{int(sp * 1000)}M"
            ngrp[key] = ngrp.get(key, 0) + n.get("port_count", 1)
        parts.append(" + ".join(f"{k}*{v}" for k, v in ngrp.items()))
    hw["config_summary"] = "/".join(parts)
    return hw


def collect_all(only=None):
    """Run all collectors and return the combined result."""
    components = {
        "cpus": [],
        "memory": [],
        "disks": [],
        "nics": [],
        "gpus": [],
        "psus": [],
        "ports": [],
        "system": {},
    }

    categories = only.split(",") if only else None

    if not categories or "system" in categories:
        components["system"] = collect_system()
    if not categories or "cpus" in categories:
        components["cpus"] = collect_cpus()
    if not categories or "memory" in categories:
        components["memory"] = collect_memory()
    if not categories or "disks" in categories:
        components["disks"] = collect_disks()
    if not categories or "nics" in categories or "ports" in categories:
        nics, ports = collect_nics_and_ports()
        if not categories or "nics" in categories:
            components["nics"] = nics
        if not categories or "ports" in categories:
            components["ports"] = ports
    if not categories or "gpus" in categories:
        components["gpus"] = collect_gpus()
    if not categories or "psus" in categories:
        components["psus"] = collect_psus()

    return components


def main():
    parser = argparse.ArgumentParser(description="Hardware inventory collector")
    parser.add_argument("--pretty", action="store_true", help="Pretty-print JSON output")
    parser.add_argument(
        "--report", metavar="URL", help="Report to PXE Station API (e.g. http://pxe:8000)"
    )
    parser.add_argument(
        "--only",
        metavar="CATEGORIES",
        help="Comma-separated: cpus,memory,disks,nics,ports,gpus,psus,system",
    )
    parser.add_argument("--summary", action="store_true", help="Also print hardware summary")
    parser.add_argument("--sn", metavar="SN", help="Serial number (auto-detected if not provided)")
    args = parser.parse_args()

    components = collect_all(only=args.only)

    if args.pretty:
        print(json.dumps(components, indent=2, ensure_ascii=False))
    else:
        print(json.dumps(components, ensure_ascii=False))

    if args.summary:
        summary = generate_summary(components)
        print("\n--- Summary ---", file=sys.stderr)
        print(json.dumps(summary, indent=2, ensure_ascii=False), file=sys.stderr)

    if args.report:
        import urllib.request

        # Detect SN
        sn = args.sn
        if not sn:
            sn = run("cat /sys/class/dmi/id/product_serial 2>/dev/null").strip()
            if not sn or sn in ("Not Specified", "NotSpecified"):
                sn = run("cat /sys/class/dmi/id/product_uuid 2>/dev/null").strip()
        if not sn:
            print("ERROR: Cannot detect SN. Use --sn to specify.", file=sys.stderr)
            sys.exit(1)

        api_url = args.report.rstrip("/")
        # Report components
        req = urllib.request.Request(
            f"{api_url}/api/assets/{sn}/components",
            data=json.dumps(components).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            resp = urllib.request.urlopen(req, timeout=10)
            print(f"Components reported: {resp.read().decode()}", file=sys.stderr)
        except Exception as e:
            print(f"ERROR reporting components: {e}", file=sys.stderr)

        # Report summary
        summary = generate_summary(components)
        req2 = urllib.request.Request(
            f"{api_url}/api/assets/{sn}/hardware",
            data=json.dumps(summary).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            resp2 = urllib.request.urlopen(req2, timeout=10)
            print(f"Summary reported: {resp2.read().decode()}", file=sys.stderr)
        except Exception as e:
            print(f"ERROR reporting summary: {e}", file=sys.stderr)


if __name__ == "__main__":
    main()
