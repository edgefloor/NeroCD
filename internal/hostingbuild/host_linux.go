package hostingbuild

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func ownedByCurrentUser(st os.FileInfo) error {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) {
		return errors.New("state must belong to engine uid")
	}
	return nil
}
func acquireHostLock() (*os.File, error) { return lockFile("/run/nerocd-hostingbuild.lock") }

func lockFile(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		_ = f.Close()
		return nil, errors.New("unsafe host lock")
	}
	if err = ownedByCurrentUser(st); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrBusy
	}
	return f, nil
}

func acquireLock(dir string) (*os.File, error) { return lockFile(filepath.Join(dir, "host.lock")) }

func freeBytes(p string) (uint64, error) {
	var s syscall.Statfs_t
	err := syscall.Statfs(p, &s)
	return s.Bavail * uint64(s.Bsize), err
}

// containment checks the actual cgroup v2 ancestor budgets, not Docker flags.
// Every builder process and descendant must remain under this enforcing group.
func containment(pid int, l Limits, aggregate string) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	group := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::/") {
			group = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if group == "" || group == "/" {
		return "", errors.New("cgroup v2 builder containment unavailable")
	}
	expected := groupRelative(aggregate)
	if expected == "" || (group != expected && !strings.HasPrefix(group, expected+"/")) {
		return "", errors.New("process is outside its exact owned aggregate")
	}
	if err = checkGroup(aggregate, l); err != nil {
		return "", err
	}
	return group, nil
}

func checkGroup(p string, l Limits) error {
	for name, want := range map[string]int64{"memory.max": l.MemoryBytes, "memory.swap.max": l.SwapBytes} {
		b, err := os.ReadFile(filepath.Join(p, name))
		if err != nil {
			return err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || n < 0 || n != want {
			return fmt.Errorf("%s not enforced: %s", name, strings.TrimSpace(string(b)))
		}
	}
	b, err := os.ReadFile(filepath.Join(p, "cpu.max"))
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return errors.New("invalid cpu.max")
	}
	q, e1 := strconv.ParseInt(f[0], 10, 64)
	period, e2 := strconv.ParseInt(f[1], 10, 64)
	if e1 != nil || e2 != nil || q <= 0 || period <= 0 || q > 1<<40 || period > 1<<30 || q != l.CPUQuota || period != 100000 {
		return errors.New("CPU aggregate quota not enforced")
	}
	return nil
}

func verifyDescendants(pid int, group string, l Limits) error {
	_, err := inspectDescendants(pid, group, l)
	return err
}

func inspectDescendants(pid int, group string, l Limits) (int, error) {
	count := 0
	members := map[int]bool{}
	if err := checkGroup(filepath.Join("/sys/fs/cgroup", group), l); err != nil {
		return count, err
	}
	err := filepath.WalkDir(filepath.Join("/sys/fs/cgroup", group), func(p string, d fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if d.Name() != "cgroup.procs" {
			return nil
		}
		b, readErr := os.ReadFile(p)
		if os.IsNotExist(readErr) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		for _, field := range strings.Fields(string(b)) {
			n, parseErr := strconv.Atoi(field)
			if parseErr != nil {
				return parseErr
			}
			members[n] = true
		}
		return nil
	})
	if err != nil {
		return count, err
	}
	count = len(members)
	// Inspect only processes inside the owned cgroup and descendants of the
	// owned builder. /proc children traversal avoids reading unrelated tasks.
	descendants, err := processDescendants(pid)
	if err != nil {
		return count, err
	}
	for n := range descendants {
		if !members[n] {
			count++
			members[n] = true
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", n))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return count, err
		}
		actual := strings.TrimSpace(strings.TrimPrefix(string(b), "0::"))
		if actual != group && !strings.HasPrefix(actual, group+"/") {
			return count, fmt.Errorf("builder descendant %d escaped enforcing cgroup", n)
		}
	}
	return count, nil
}

func processDescendants(pid int) (map[int]bool, error) {
	found := map[int]bool{}
	queue := []int{pid}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if found[current] {
			continue
		}
		found[current] = true
		threads, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", current))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, thread := range threads {
			children, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", current, thread.Name()))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, child := range strings.Fields(string(children)) {
				n, err := strconv.Atoi(child)
				if err != nil {
					return nil, err
				}
				if !found[n] {
					queue = append(queue, n)
				}
			}
		}
	}
	return found, nil
}

func groupRelative(p string) string {
	relative, err := filepath.Rel("/sys/fs/cgroup", p)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return ""
	}
	return "/" + relative
}
func validateDelegation(root string) error {
	if !filepath.IsAbs(root) || root != filepath.Clean(root) || !strings.HasPrefix(root, "/sys/fs/cgroup/") {
		return errors.New("explicit delegated cgroup v2 root required")
	}
	for p := root; p != "/sys/fs/cgroup"; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe delegated cgroup path")
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return err
	}
	if stat.Type != 0x63677270 {
		return errors.New("delegation must be on cgroup v2 filesystem")
	}
	kind, err := os.ReadFile(filepath.Join(root, "cgroup.type"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(kind)) != "domain" {
		return errors.New("delegated cgroup must be a domain")
	}
	processes, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(processes)) != "" {
		return errors.New("delegated root must contain no direct processes")
	}
	controllers, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
	if err != nil {
		return err
	}
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if !strings.Contains(" "+strings.TrimSpace(string(controllers))+" ", " "+controller+" ") {
			return errors.New("delegated cpu/memory/pids controllers must already be enabled")
		}
	}
	return nil
}
func createBuildGroup(p string, l Limits) error {
	if err := os.Mkdir(p, 0700); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"cpu.max":                strconv.FormatInt(l.CPUQuota, 10) + " 100000",
		"memory.max":             strconv.FormatInt(l.MemoryBytes, 10),
		"memory.swap.max":        strconv.FormatInt(l.SwapBytes, 10),
		"pids.max":               "4096",
		"cgroup.subtree_control": "+cpu +memory +pids",
	} {
		if err := os.WriteFile(filepath.Join(p, name), []byte(value), 0600); err != nil {
			return err
		}
	}
	return checkGroup(p, l)
}
func (e *Engine) cleanupGroup(r ownedResources) error {
	if r.Group == "" {
		return nil
	}
	if len(r.Token) != 32 || r.Group != filepath.Join(e.config.CgroupRoot, "build-"+r.Token) {
		return errors.New("cleanup refused foreign cgroup")
	}
	if _, err := os.Lstat(r.Group); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	// This path names only the exact per-build group created before Docker work.
	// Kernel cgroup.kill stops any owned orphan that survived Docker shutdown.
	if err := os.WriteFile(filepath.Join(r.Group, "cgroup.kill"), []byte("1"), 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		events, err := os.ReadFile(filepath.Join(r.Group, "cgroup.events"))
		if err != nil {
			return err
		}
		if strings.Contains(string(events), "populated 0") {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("owned cgroup termination unconfirmed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var dirs []string
	err := filepath.WalkDir(r.Group, func(p string, d fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			b, err := os.ReadFile(filepath.Join(p, "cgroup.procs"))
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(b)) != "" {
				return errors.New("owned cgroup still populated")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = os.Remove(dirs[i]); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
