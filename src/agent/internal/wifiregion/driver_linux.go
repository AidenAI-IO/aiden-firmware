//go:build linux

package wifiregion

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

const (
	aicVendorID         = 0x001a11
	aicSetCountrySubcmd = 0x100e
	aicCountryAttribute = 4
	netlinkNestedFlag   = 1 << 15
	netlinkHeaderSize   = 16
	genericHeaderSize   = 4
	netlinkReceiveLimit = 64 * 1024
)

// ApplyDriverCountry changes the AIC8800 wiphy's self-managed regulatory
// domain. The driver requires a nested vendor-data attribute; iw's vendor send
// command sends raw data and is rejected by this kernel's nl80211 validation.
func ApplyDriverCountry(ctx context.Context, interfaceName, country string) error {
	code, ok := Normalize(country)
	if !ok {
		return fmt.Errorf("unsupported Wi-Fi country %q", country)
	}
	index, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return fmt.Errorf("find Wi-Fi interface: %w", err)
	}
	client, err := openRegulatoryNetlink()
	if err != nil {
		return err
	}
	defer client.close()
	attributes := appendNetlinkAttribute(nil, unix.NL80211_ATTR_IFINDEX, nativeUint32(uint32(index.Index)))
	attributes = appendNetlinkAttribute(attributes, unix.NL80211_ATTR_VENDOR_ID, nativeUint32(aicVendorID))
	attributes = appendNetlinkAttribute(attributes, unix.NL80211_ATTR_VENDOR_SUBCMD, nativeUint32(aicSetCountrySubcmd))
	inner := appendNetlinkAttribute(nil, aicCountryAttribute, []byte(code+"\x00"))
	attributes = appendNetlinkAttribute(attributes, unix.NL80211_ATTR_VENDOR_DATA|netlinkNestedFlag, inner)
	if _, err := client.request(ctx, client.family, unix.NL80211_CMD_VENDOR, attributes); err != nil {
		return fmt.Errorf("set AIC8800 country to %s: %w", code, err)
	}
	return nil
}

// ReadDriverCountry queries the per-wiphy domain used by wlan0. The global
// cfg80211 domain can stay at 00 even when the AIC8800 is correctly set.
func ReadDriverCountry(ctx context.Context, interfaceName string) (string, error) {
	index, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return "", fmt.Errorf("find Wi-Fi interface: %w", err)
	}
	client, err := openRegulatoryNetlink()
	if err != nil {
		return "", err
	}
	defer client.close()
	responses, err := client.request(ctx, client.family, unix.NL80211_CMD_GET_INTERFACE,
		appendNetlinkAttribute(nil, unix.NL80211_ATTR_IFINDEX, nativeUint32(uint32(index.Index))))
	if err != nil {
		return "", fmt.Errorf("query Wi-Fi wiphy: %w", err)
	}
	var wiphy uint32
	foundWiphy := false
	for _, response := range responses {
		for _, attribute := range decodeNetlinkAttributes(response[genericHeaderSize:]) {
			if attribute.kind == unix.NL80211_ATTR_WIPHY && len(attribute.value) >= 4 {
				wiphy = binary.NativeEndian.Uint32(attribute.value)
				foundWiphy = true
			}
		}
	}
	if !foundWiphy {
		return "", errors.New("Wi-Fi interface response has no wiphy")
	}
	responses, err = client.request(ctx, client.family, unix.NL80211_CMD_GET_REG,
		appendNetlinkAttribute(nil, unix.NL80211_ATTR_WIPHY, nativeUint32(wiphy)))
	if err != nil {
		return "", fmt.Errorf("query Wi-Fi regulatory domain: %w", err)
	}
	for _, response := range responses {
		for _, attribute := range decodeNetlinkAttributes(response[genericHeaderSize:]) {
			if attribute.kind == unix.NL80211_ATTR_REG_ALPHA2 && len(attribute.value) >= 2 {
				return string(attribute.value[:2]), nil
			}
		}
	}
	return "", errors.New("Wi-Fi regulatory response has no country")
}

type regulatoryNetlink struct {
	fd     int
	pid    uint32
	family uint16
	seq    uint32
}

func openRegulatoryNetlink() (*regulatoryNetlink, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_GENERIC)
	if err != nil {
		return nil, fmt.Errorf("open generic netlink: %w", err)
	}
	client := &regulatoryNetlink{fd: fd}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		client.close()
		return nil, fmt.Errorf("bind generic netlink: %w", err)
	}
	address, err := unix.Getsockname(fd)
	if err != nil {
		client.close()
		return nil, fmt.Errorf("read generic netlink port: %w", err)
	}
	client.pid = address.(*unix.SockaddrNetlink).Pid
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO,
		&unix.Timeval{Sec: 5}); err != nil {
		client.close()
		return nil, fmt.Errorf("set generic netlink timeout: %w", err)
	}
	responses, err := client.request(context.Background(), unix.GENL_ID_CTRL,
		unix.CTRL_CMD_GETFAMILY,
		appendNetlinkAttribute(nil, unix.CTRL_ATTR_FAMILY_NAME, []byte("nl80211\x00")))
	if err != nil {
		client.close()
		return nil, fmt.Errorf("find nl80211 family: %w", err)
	}
	for _, response := range responses {
		for _, attribute := range decodeNetlinkAttributes(response[genericHeaderSize:]) {
			if attribute.kind == unix.CTRL_ATTR_FAMILY_ID && len(attribute.value) >= 2 {
				client.family = binary.NativeEndian.Uint16(attribute.value)
			}
		}
	}
	if client.family == 0 {
		client.close()
		return nil, errors.New("nl80211 family ID missing")
	}
	return client, nil
}

func (client *regulatoryNetlink) close() { _ = unix.Close(client.fd) }

func (client *regulatoryNetlink) request(ctx context.Context, family uint16, command uint8, attributes []byte) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client.seq++
	seq := client.seq
	payload := make([]byte, netlinkHeaderSize+genericHeaderSize+len(attributes))
	binary.NativeEndian.PutUint32(payload[0:4], uint32(len(payload)))
	binary.NativeEndian.PutUint16(payload[4:6], family)
	binary.NativeEndian.PutUint16(payload[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.NativeEndian.PutUint32(payload[8:12], seq)
	binary.NativeEndian.PutUint32(payload[12:16], client.pid)
	payload[16], payload[17] = command, 1
	copy(payload[netlinkHeaderSize+genericHeaderSize:], attributes)
	if err := unix.Sendto(client.fd, payload, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	var responses [][]byte
	buffer := make([]byte, netlinkReceiveLimit)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, _, err := unix.Recvfrom(client.fd, buffer, 0)
		if err != nil {
			return nil, err
		}
		for offset := 0; offset+netlinkHeaderSize <= count; {
			length := int(binary.NativeEndian.Uint32(buffer[offset : offset+4]))
			if length < netlinkHeaderSize || offset+length > count {
				return nil, errors.New("malformed netlink response")
			}
			kind := binary.NativeEndian.Uint16(buffer[offset+4 : offset+6])
			messageSeq := binary.NativeEndian.Uint32(buffer[offset+8 : offset+12])
			body := buffer[offset+netlinkHeaderSize : offset+length]
			offset += alignNetlink(length)
			if messageSeq != seq {
				continue
			}
			if kind == unix.NLMSG_ERROR {
				if len(body) < 4 {
					return nil, errors.New("short netlink acknowledgement")
				}
				status := int32(binary.NativeEndian.Uint32(body[:4]))
				if status != 0 {
					return nil, unix.Errno(-status)
				}
				return responses, nil
			}
			if kind == unix.NLMSG_DONE {
				return responses, nil
			}
			if len(body) < genericHeaderSize {
				return nil, errors.New("short generic netlink response")
			}
			responses = append(responses, append([]byte(nil), body...))
		}
	}
}

type netlinkAttribute struct {
	kind  uint16
	value []byte
}

func decodeNetlinkAttributes(data []byte) []netlinkAttribute {
	var attributes []netlinkAttribute
	for len(data) >= 4 {
		length := int(binary.NativeEndian.Uint16(data[:2]))
		if length < 4 || length > len(data) {
			break
		}
		attributes = append(attributes, netlinkAttribute{
			kind:  binary.NativeEndian.Uint16(data[2:4]) &^ netlinkNestedFlag,
			value: data[4:length],
		})
		step := alignNetlink(length)
		if step > len(data) {
			break
		}
		data = data[step:]
	}
	return attributes
}

func appendNetlinkAttribute(dst []byte, kind uint16, value []byte) []byte {
	length := 4 + len(value)
	start := len(dst)
	dst = append(dst, make([]byte, alignNetlink(length))...)
	binary.NativeEndian.PutUint16(dst[start:start+2], uint16(length))
	binary.NativeEndian.PutUint16(dst[start+2:start+4], kind)
	copy(dst[start+4:], value)
	return dst
}

func nativeUint32(value uint32) []byte {
	data := make([]byte, 4)
	binary.NativeEndian.PutUint32(data, value)
	return data
}

func alignNetlink(length int) int { return (length + 3) &^ 3 }
