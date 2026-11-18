package system_probe

import (
	"strconv"
	"strings"
)

// parseDiskstats 解析 /proc/diskstats，过滤 loop/ram/fd/sr 设备，扇区按 512B 换算。
func parseDiskstats(data []byte) []DiskDevice {
	devices := make([]DiskDevice, 0, maxDiskDevices)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if skipDiskDevice(name) {
			continue
		}
		values := make([]uint64, 11)
		valid := true
		for i := 0; i < 11; i++ {
			value, err := strconv.ParseUint(fields[i+3], 10, 64)
			if err != nil {
				valid = false
				break
			}
			values[i] = value
		}
		if !valid {
			continue
		}
		devices = append(devices, DiskDevice{
			Name:             name,
			ReadsCompleted:   values[0],
			ReadsMerged:      values[1],
			SectorsRead:      values[2],
			ReadBytes:        values[2] * 512,
			ReadTimeMS:       values[3],
			WritesCompleted:  values[4],
			WritesMerged:     values[5],
			SectorsWritten:   values[6],
			WriteBytes:       values[6] * 512,
			WriteTimeMS:      values[7],
			IOInProgress:     values[8],
			IOTimeMS:         values[9],
			WeightedIOTimeMS: values[10],
		})
	}
	return devices
}

func skipDiskDevice(name string) bool {
	for _, prefix := range []string{"loop", "ram", "fd", "sr"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
