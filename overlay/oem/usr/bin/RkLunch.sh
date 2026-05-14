#!/bin/sh

if [ -f "/oem/usr/ko/insmod_ko.sh" ]; then
	cd /oem/usr/ko && sh insmod_ko.sh
fi
