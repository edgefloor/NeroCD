package serverbootstrap

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// State describes one prerequisite, never an aggregate server readiness decision.
type State string

// Prerequisite states distinguish absence, temporary failure, and incompatibility.
const (
	Available   State = "available"
	Missing     State = "missing"
	Unavailable State = "unavailable"
	Unsupported State = "unsupported"
)

// SystemdStatus records a fixed interpretation of PID 1 and systemctl state.
type SystemdStatus string

// Supported systemd observations contain no raw status text.
const (
	SystemdRunning    SystemdStatus = "running"
	SystemdDegraded   SystemdStatus = "degraded"
	SystemdStarting   SystemdStatus = "starting"
	SystemdOffline    SystemdStatus = "offline"
	SystemdNotSystemd SystemdStatus = "not_systemd"
	SystemdMissing    SystemdStatus = "missing"
)

// Observations is an atomic prerequisite snapshot, not enrolled identity or TLS evidence.
type Observations struct {
	OS      OSObservation
	Systemd SystemdObservation
	Docker  DockerObservation
	Compose VersionObservation
	Buildx  VersionObservation
	Disk    DiskObservation
}

// OSObservation contains bounded metadata tokens and a normalized native architecture.
type OSObservation struct {
	State        State
	ID           string
	VersionID    string
	Architecture string
}

// SystemdObservation distinguishes real PID 1 readiness from an installed command.
type SystemdObservation struct {
	State  State
	Status SystemdStatus
}

// DockerObservation describes the target's local Unix-socket daemon API.
type DockerObservation struct {
	State      State
	APIVersion string
}

// VersionObservation describes a plugin from fixed system installation paths.
type VersionObservation struct {
	State   State
	Version string
}

// DiskObservation contains root filesystem bytes observed through POSIX df.
type DiskObservation struct {
	State          State
	TotalBytes     uint64
	AvailableBytes uint64
}

var (
	metadataToken = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,31}$`)
	versionToken  = regexp.MustCompile(`^v?[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}([+-][a-zA-Z0-9][a-zA-Z0-9.+-]{0,47})?$`)
	apiToken      = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}$`)
)

func parseObservations(data []byte) (Observations, error) {
	var result Observations
	lines := strings.Split(string(data), "\n")
	if len(lines) != 9 || lines[0] != "nerocd_probe_v1" || lines[7] != "end" || lines[8] != "" {
		return Observations{}, InvalidOutput
	}
	fields := make([][]string, 6)
	for index := range fields {
		fields[index] = strings.Split(lines[index+1], "\t")
	}
	os := fields[0]
	if len(os) != 5 || os[0] != "os" {
		return Observations{}, InvalidOutput
	}
	result.OS.State = State(os[1])
	switch result.OS.State {
	case Available:
		if !metadataToken.MatchString(os[2]) || !metadataToken.MatchString(os[3]) {
			return Observations{}, InvalidOutput
		}
		result.OS.ID, result.OS.VersionID = os[2], os[3]
	case Missing, Unsupported:
		if os[2] != "-" || os[3] != "-" {
			return Observations{}, InvalidOutput
		}
	default:
		return Observations{}, InvalidOutput
	}
	switch os[4] {
	case "x86_64", "amd64":
		result.OS.Architecture = "amd64"
	case "aarch64", "arm64":
		result.OS.Architecture = "arm64"
	default:
		if !metadataToken.MatchString(os[4]) {
			return Observations{}, InvalidOutput
		}
		result.OS.Architecture = "unsupported"
		result.OS.State = Unsupported
	}
	systemd := fields[1]
	if len(systemd) != 2 || systemd[0] != "systemd" {
		return Observations{}, InvalidOutput
	}
	result.Systemd.Status = SystemdStatus(systemd[1])
	switch result.Systemd.Status {
	case SystemdRunning:
		result.Systemd.State = Available
	case SystemdDegraded, SystemdStarting, SystemdOffline:
		result.Systemd.State = Unavailable
	case SystemdNotSystemd:
		result.Systemd.State = Unsupported
	case SystemdMissing:
		result.Systemd.State = Missing
	default:
		return Observations{}, InvalidOutput
	}
	docker := fields[2]
	if len(docker) != 3 || docker[0] != "docker" {
		return Observations{}, InvalidOutput
	}
	result.Docker.State = State(docker[1])
	if result.Docker.State == Available {
		if !apiToken.MatchString(docker[2]) {
			return Observations{}, InvalidOutput
		}
		result.Docker.APIVersion = docker[2]
	} else if (result.Docker.State != Missing && result.Docker.State != Unavailable) || docker[2] != "-" {
		return Observations{}, InvalidOutput
	}
	for index, name := range []string{"compose", "buildx"} {
		plugin := fields[index+3]
		if len(plugin) != 3 || plugin[0] != name {
			return Observations{}, InvalidOutput
		}
		observation := VersionObservation{State: State(plugin[1])}
		if observation.State == Available {
			if !versionToken.MatchString(plugin[2]) {
				return Observations{}, InvalidOutput
			}
			observation.Version = plugin[2]
		} else if (observation.State != Missing && observation.State != Unavailable) || plugin[2] != "-" {
			return Observations{}, InvalidOutput
		}
		if index == 0 {
			result.Compose = observation
		} else {
			result.Buildx = observation
		}
	}
	disk := fields[5]
	if len(disk) != 4 || disk[0] != "disk" {
		return Observations{}, InvalidOutput
	}
	result.Disk.State = State(disk[1])
	if result.Disk.State != Available {
		if (result.Disk.State != Missing && result.Disk.State != Unavailable) || disk[2] != "-" || disk[3] != "-" {
			return Observations{}, InvalidOutput
		}
		return result, nil
	}
	total, totalErr := strconv.ParseUint(disk[2], 10, 64)
	available, availableErr := strconv.ParseUint(disk[3], 10, 64)
	if totalErr != nil || availableErr != nil || total == 0 || available > total || total > math.MaxUint64/1024 {
		return Observations{}, InvalidOutput
	}
	result.Disk.TotalBytes, result.Disk.AvailableBytes = total*1024, available*1024
	return result, nil
}
