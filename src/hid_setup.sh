mount -t configfs none /sys/kernel/config
UDC=$(ls /sys/class/udc | head -n1)
./example_usb_hid --udc "$UDC" --force setup composite
