package system_probe

import (
	"bufio"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
)

// parseNetDev 解析 /proc/net/dev；列不足的接口用零值补齐。
func parseNetDev(data []byte) []NetworkInterface {
	out := make([]NetworkInterface, 0, maxNetworkIfaces)
	for _, line := range strings.Split(string(data), "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		if name == "" || name == "Interfaces" || strings.Contains(line, "|") {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		values := make([]uint64, 16)
		for i := 0; i < len(values) && i < len(fields); i++ {
			value, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				values[i] = 0
				continue
			}
			values[i] = value
		}
		out = append(out, NetworkInterface{
			Name: name, RXBytes: values[0], RXPackets: values[1], RXErrors: values[2],
			RXDropped: values[3], RXFIFO: values[4], RXFrame: values[5],
			RXCompressed: values[6], RXMulticast: values[7], TXBytes: values[8],
			TXPackets: values[9], TXErrors: values[10], TXDropped: values[11],
			TXFIFO: values[12], TXColls: values[13], TXCarrier: values[14],
			TXCompressed: values[15],
		})
	}
	return out
}

// parseSockstat 解析 /proc/net/sockstat 或 sockstat6 并累加到 out。
func parseSockstat(data []byte, out *Sockstat) {
	if out == nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		kind := strings.TrimSuffix(fields[0], ":")
		switch kind {
		case "sockets":
			out.SocketsUsed += pairValue(fields[1:], "used")
		case "TCP", "TCP6":
			out.TCPInUse += pairValue(fields[1:], "inuse")
			out.TCPOrphan += pairValue(fields[1:], "orphan")
			out.TCPTW += pairValue(fields[1:], "tw")
			out.TCPAlloc += pairValue(fields[1:], "alloc")
			out.TCPMemPages += pairValue(fields[1:], "mem")
		case "UDP", "UDP6":
			out.UDPInUse += pairValue(fields[1:], "inuse")
			out.UDPMemPages += pairValue(fields[1:], "mem")
		case "RAW", "RAW6":
			out.RawInUse += pairValue(fields[1:], "inuse")
		case "FRAG", "FRAG6":
			out.FragInUse += pairValue(fields[1:], "inuse")
		}
	}
	pageSize := int64(os.Getpagesize())
	out.TCPMemBytes = out.TCPMemPages * pageSize
	out.UDPMemBytes = out.UDPMemPages * pageSize
}

func pairValue(fields []string, key string) int64 {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] != key {
			continue
		}
		value, err := strconv.ParseInt(fields[i+1], 10, 64)
		if err != nil {
			return 0
		}
		return value
	}
	return 0
}

// countTCPStates 流式统计 /proc/net/tcp(6) 的 st 字段；每 4096 行检查上下文。
func countTCPStates(ctx context.Context, reader io.Reader, maxLines int) (TCPStates, int64, bool, error) {
	var states TCPStates
	if reader == nil {
		return states, 0, false, nil
	}
	if maxLines <= 0 {
		maxLines = maxProcNetLines
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, limitProcNetFile))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var scanned int64
	lineNo := 0
	truncated := false
	for scanner.Scan() {
		lineNo++
		if lineNo%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return states, scanned, true, err
			}
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[0] == "sl" {
			continue
		}
		if scanned >= int64(maxLines) {
			truncated = true
			break
		}
		scanned++
		countTCPState(&states, fields[3])
	}
	if err := scanner.Err(); err != nil {
		return states, scanned, truncated, err
	}
	return states, scanned, truncated, nil
}

func countTCPState(states *TCPStates, raw string) {
	value, err := strconv.ParseUint(raw, 16, 8)
	if err != nil {
		states.Unknown++
		return
	}
	switch value {
	case 0x01:
		states.Established++
	case 0x02:
		states.SynSent++
	case 0x03:
		states.SynRecv++
	case 0x04:
		states.FinWait1++
	case 0x05:
		states.FinWait2++
	case 0x06:
		states.TimeWait++
	case 0x07:
		states.Close++
	case 0x08:
		states.CloseWait++
	case 0x09:
		states.LastAck++
	case 0x0A:
		states.Listen++
	case 0x0B:
		states.Closing++
	default:
		states.Unknown++
	}
}
