#!/bin/bash
set -eux
input="/etc/sriov_config.json"
UDEV_RULE_FILE='/etc/udev/rules.d/10-persistent-net.rules'

if [ ! -f $input ]; then
  echo "File /etc/sriov_config.json not exist."
  exit
fi

append_to_file(){
  content="$1"
  file_name="$2"
  if ! test -f "$file_name"
  then
    echo "$content" > "$file_name"
  else
    if ! grep -Fxq "$content" "$file_name"
    then
      echo "$content" >> "$file_name"
    fi
  fi
}

add_udev_rule_for_sriov_pf(){
    pf_pci=$(grep PCI_SLOT_NAME /sys/class/net/$1/device/uevent | cut -d'=' -f2)
    udev_data_line="SUBSYSTEM==\"net\", ACTION==\"add\", DRIVERS==\"?*\", KERNELS==\"$pf_pci\", NAME=\"$1\""
    append_to_file "$udev_data_line" "$UDEV_RULE_FILE"
}

jq -c '.interfaces[]' $input | while read iface;
do
  eswitch_mode=$(echo $iface | jq '.eSwitchMode' -r)
  if [[ "$eswitch_mode" == "switchdev" ]]; then
    pci_addr=$(echo $iface | jq '.pciAddress' -r)
    name=$(echo $iface | jq '.name' -r)

    # Create udev rule to save PF name
    # add_udev_rule_for_sriov_pf $name

    sleep 5

    # set PF to switchdev mode
    devlink dev eswitch set pci/${pci_addr} mode switchdev
    # ip link set ${name} up

    # turn hw-tc-offload on
    # /usr/sbin/ethtool -K ${name} hw-tc-offload on
  fi
done
