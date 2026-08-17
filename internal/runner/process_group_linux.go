//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processGroupAlive ignores zombie-only groups. Containers commonly run under
// a PID 1 that does not promptly reap children; kill(-pgid, 0) still reports
// those zombies even though no runnable descendant remains.
func processGroupAlive(pgid int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		state, memberGroup, exists, err := linuxProcessState(pid)
		if err != nil {
			return false, err
		}
		if exists && memberGroup == pgid && state != "Z" {
			return true, nil
		}
	}
	return false, nil
}

func linuxProcessState(pid int) (state string, pgrp int, exists bool, err error) {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	state, pgrp, err = parseLinuxProcessStat(string(contents))
	if err != nil {
		return "", 0, false, err
	}
	return state, pgrp, true, nil
}

func parseLinuxProcessStat(stat string) (state string, pgrp int, err error) {
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 {
		return "", 0, fmt.Errorf("malformed /proc stat: missing command terminator")
	}
	fields := strings.Fields(stat[closeParen+1:])
	if len(fields) < 3 {
		return "", 0, fmt.Errorf("malformed /proc stat: expected state, ppid, pgrp")
	}
	pgrp, err = strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, fmt.Errorf("malformed /proc stat pgrp: %w", err)
	}
	return fields[0], pgrp, nil
}
