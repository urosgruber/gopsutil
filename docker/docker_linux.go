// +build linux

package docker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	cpu "github.com/DataDog/gopsutil/cpu"
	"github.com/DataDog/gopsutil/internal/common"
)

// GetDockerStat returns a list of Docker basic stats.
// This requires certain permission.
func GetDockerStat() ([]CgroupDockerStat, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrDockerNotAvailable
	}

	out, err := invoke.Command(path, "ps", "-a", "--no-trunc", "--format", "{{.ID}}|{{.Image}}|{{.Names}}|{{.Status}}")
	if err != nil {
		return []CgroupDockerStat{}, err
	}
	lines := strings.Split(string(out), "\n")
	ret := make([]CgroupDockerStat, 0, len(lines))

	for _, l := range lines {
		if l == "" {
			continue
		}
		cols := strings.Split(l, "|")
		if len(cols) != 4 {
			continue
		}
		names := strings.Split(cols[2], ",")
		stat := CgroupDockerStat{
			ContainerID: cols[0],
			Name:        names[0],
			Image:       cols[1],
			Status:      cols[3],
			Running:     strings.Contains(cols[3], "Up"),
		}
		ret = append(ret, stat)
	}

	return ret, nil
}

// Generates a mapping of PIDs to container metadata.
func GetContainerStatsByPID() (map[int32]ContainerStat, error) {
	containerMap := make(map[int32]ContainerStat)
	path, err := getCgroupMountPoint("cpuacct")
	if err != nil {
		return nil, err
	}
	if common.PathExists(path) {
		contents, err := common.ListDirectory(path)
		if err != nil {
			return nil, err
		}

		// If docker containers exist, collect their stats.
		if common.StringsContains(contents, "docker") {
			dockerStats, err := GetDockerStat()
			if err == nil {
				for _, dockerStat := range dockerStats {
					if !dockerStat.Running {
						continue
					}

					dockerPids, err := CgroupPIDsDocker(dockerStat.ContainerID)
					if err != nil {
						continue
					}

					containerStat := ContainerStat{
						Type:  "Docker",
						Name:  dockerStat.Name,
						ID:    dockerStat.ContainerID,
						Image: dockerStat.Image,
					}

					for _, pid := range dockerPids {
						containerMap[pid] = containerStat
					}
				}
			}
		}
	}

	return containerMap, nil
}

func (c CgroupDockerStat) String() string {
	s, _ := json.Marshal(c)
	return string(s)
}

// GetDockerIDList returnes a list of DockerID.
// This requires certain permission.
func GetDockerIDList() ([]string, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrDockerNotAvailable
	}

	out, err := invoke.Command(path, "ps", "-q", "--no-trunc")
	if err != nil {
		return []string{}, err
	}
	lines := strings.Split(string(out), "\n")
	ret := make([]string, 0, len(lines))

	for _, l := range lines {
		if l == "" {
			continue
		}
		ret = append(ret, l)
	}

	return ret, nil
}

// CgroupCPU returnes specified cgroup id CPU status.
// containerID is same as docker id if you use docker.
// If you use container via systemd.slice, you could use
// containerID = docker-<container id>.scope and base=/sys/fs/cgroup/cpuacct/system.slice/
func CgroupCPU(containerID string, base string) (*cpu.TimesStat, error) {
	statfile := getCgroupFilePath(containerID, base, "cpuacct", "cpuacct.stat")
	lines, err := common.ReadLines(statfile)
	if err != nil {
		return nil, err
	}
	// empty containerID means all cgroup
	if len(containerID) == 0 {
		containerID = "all"
	}
	ret := &cpu.TimesStat{CPU: containerID}
	for _, line := range lines {
		fields := strings.Split(line, " ")
		if fields[0] == "user" {
			user, err := strconv.ParseFloat(fields[1], 64)
			if err == nil {
				ret.User = float64(user)
			}
		}
		if fields[0] == "system" {
			system, err := strconv.ParseFloat(fields[1], 64)
			if err == nil {
				ret.System = float64(system)
			}
		}
	}

	return ret, nil
}

func CgroupCPUDocker(containerID string) (*cpu.TimesStat, error) {
	p, err := getCgroupMountPoint("cpuacct")
	if err != nil {
		return nil, err
	}
	return CgroupCPU(containerID, filepath.Join(p, "docker"))
}

// CgroupPIDs retrieves the PIDs running within a given container.
func CgroupPIDs(containerID string, base string) ([]int32, error) {
	statfile := getCgroupFilePath(containerID, base, "cpuacct", "cgroup.procs")
	lines, err := common.ReadLines(statfile)
	if err != nil {
		return nil, err
	}

	pids := make([]int32, 0, len(lines))
	for _, line := range lines {
		pid, err := strconv.Atoi(line)
		if err == nil {
			pids = append(pids, int32(pid))
		}
	}

	return pids, nil
}

func CgroupPIDsDocker(containerID string) ([]int32, error) {
	p, err := getCgroupMountPoint("cpuacct")
	if err != nil {
		return []int32{}, err
	}
	return CgroupPIDs(containerID, filepath.Join(p, "docker"))
}

func CgroupMem(containerID string, base string) (*CgroupMemStat, error) {
	statfile := getCgroupFilePath(containerID, base, "memory", "memory.stat")

	// empty containerID means all cgroup
	if len(containerID) == 0 {
		containerID = "all"
	}
	lines, err := common.ReadLines(statfile)
	if err != nil {
		return nil, err
	}
	ret := &CgroupMemStat{ContainerID: containerID}
	for _, line := range lines {
		fields := strings.Split(line, " ")
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "cache":
			ret.Cache = v
		case "rss":
			ret.RSS = v
		case "rssHuge":
			ret.RSSHuge = v
		case "mappedFile":
			ret.MappedFile = v
		case "pgpgin":
			ret.Pgpgin = v
		case "pgpgout":
			ret.Pgpgout = v
		case "pgfault":
			ret.Pgfault = v
		case "pgmajfault":
			ret.Pgmajfault = v
		case "inactiveAnon":
			ret.InactiveAnon = v
		case "activeAnon":
			ret.ActiveAnon = v
		case "inactiveFile":
			ret.InactiveFile = v
		case "activeFile":
			ret.ActiveFile = v
		case "unevictable":
			ret.Unevictable = v
		case "hierarchicalMemoryLimit":
			ret.HierarchicalMemoryLimit = v
		case "totalCache":
			ret.TotalCache = v
		case "totalRss":
			ret.TotalRSS = v
		case "totalRssHuge":
			ret.TotalRSSHuge = v
		case "totalMappedFile":
			ret.TotalMappedFile = v
		case "totalPgpgin":
			ret.TotalPgpgIn = v
		case "totalPgpgout":
			ret.TotalPgpgOut = v
		case "totalPgfault":
			ret.TotalPgFault = v
		case "totalPgmajfault":
			ret.TotalPgMajFault = v
		case "totalInactiveAnon":
			ret.TotalInactiveAnon = v
		case "totalActiveAnon":
			ret.TotalActiveAnon = v
		case "totalInactiveFile":
			ret.TotalInactiveFile = v
		case "totalActiveFile":
			ret.TotalActiveFile = v
		case "totalUnevictable":
			ret.TotalUnevictable = v
		}
	}

	r, err := getCgroupMemFile(containerID, base, "memory.usage_in_bytes")
	if err == nil {
		ret.MemUsageInBytes = r
	}
	r, err = getCgroupMemFile(containerID, base, "memory.max_usage_in_bytes")
	if err == nil {
		ret.MemMaxUsageInBytes = r
	}
	r, err = getCgroupMemFile(containerID, base, "memoryLimitInBbytes")
	if err == nil {
		ret.MemLimitInBytes = r
	}
	r, err = getCgroupMemFile(containerID, base, "memoryFailcnt")
	if err == nil {
		ret.MemFailCnt = r
	}

	return ret, nil
}

func CgroupMemDocker(containerID string) (*CgroupMemStat, error) {
	p, err := getCgroupMountPoint("memory")
	if err != nil {
		return nil, err
	}
	return CgroupMem(containerID, filepath.Join(p, "docker"))
}

func (m CgroupMemStat) String() string {
	s, _ := json.Marshal(m)
	return string(s)
}

// getCgroupFilePath constructs file path to get targetted stats file.
func getCgroupFilePath(containerID, base, target, file string) string {
	if len(base) == 0 {
		base, _ = getCgroupMountPoint(target)
		base = filepath.Join(base, "docker")
	}
	statfile := filepath.Join(base, containerID, file)

	if _, err := os.Stat(statfile); os.IsNotExist(err) {
		base, _ = getCgroupMountPoint(target)
		statfile = filepath.Join(base, "system.slice", fmt.Sprintf("docker-%s.scope", containerID), file)
	}

	return statfile
}

// getCgroupMemFile reads a cgroup file and return the contents as uint64.
func getCgroupMemFile(containerID, base, file string) (uint64, error) {
	statfile := getCgroupFilePath(containerID, base, "memory", file)
	lines, err := common.ReadLines(statfile)
	if err != nil {
		return 0, err
	}
	if len(lines) != 1 {
		return 0, fmt.Errorf("wrong format file: %s", statfile)
	}
	return strconv.ParseUint(lines[0], 10, 64)
}

// function to get the mount point of cgroup. by default it should be under /sys/fs/cgroup but
// it could be mounted anywhere else if manually defined. Example cgroup entries in /proc/mounts would be
//	 cgroup /sys/fs/cgroup/cpuset cgroup rw,relatime,cpuset 0 0
//	 cgroup /sys/fs/cgroup/cpu cgroup rw,relatime,cpu 0 0
//	 cgroup /sys/fs/cgroup/cpuacct cgroup rw,relatime,cpuacct 0 0
//	 cgroup /sys/fs/cgroup/memory cgroup rw,relatime,memory 0 0
//	 cgroup /sys/fs/cgroup/devices cgroup rw,relatime,devices 0 0
//	 cgroup /sys/fs/cgroup/freezer cgroup rw,relatime,freezer 0 0
//	 cgroup /sys/fs/cgroup/blkio cgroup rw,relatime,blkio 0 0
//	 cgroup /sys/fs/cgroup/perf_event cgroup rw,relatime,perf_event 0 0
//	 cgroup /sys/fs/cgroup/hugetlb cgroup rw,relatime,hugetlb 0 0
// examples of target would be:
// blkio  cpu  cpuacct  cpuset  devices  freezer  hugetlb  memory  net_cls  net_prio  perf_event  pids  systemd
func getCgroupMountPoint(target string) (string, error) {
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	cgroups := []string{}
	// get all cgroup entries
	for scanner.Scan() {
		text := scanner.Text()
		if strings.HasPrefix(text, "cgroup ") {
			cgroups = append(cgroups, text)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	// old cgroup style
	if len(cgroups) == 1 {
		tokens := strings.Split(cgroups[0], " ")
		return tokens[1], nil
	}

	var candidate string
	for _, cgroup := range cgroups {
		tokens := strings.Split(cgroup, " ")
		// see if the target is the suffix of the mount directory
		if strings.Contains(tokens[1], target) {
			candidate = tokens[1]
		}
	}
	if candidate == "" {
		return candidate, fmt.Errorf("Mount point for cgroup %s is not found!", target)
	}
	return candidate, nil
}
