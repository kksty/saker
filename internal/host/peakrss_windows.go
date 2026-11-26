//go:build windows

package host

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sort"
	"unsafe"

	"golang.org/x/sys/windows"
)

// childPeakRSS reads the peak working set of every process in this instance's
// job object. That is the same handle that kills the tree when saker exits, so the
// job already lists exactly what this instance started plus every descendant the
// harness spawned — including a PoC interpreter a tool call launched, which is
// the process that grew to tens of gigabytes in the incident that motivated
// this.
//
// roots (the supervised Hub/scan pids) are merged in because a job query can
// fail — a container or an outer runner may already have put this process in a
// job. In that case the two long-lived children are still readable directly, and
// the failed enumeration is reported instead of silently skipped.
func childPeakRSS(roots []int) []PeakRSS {
	pids := map[int]bool{}
	for _, pid := range roots {
		if pid > 0 {
			pids[pid] = true
		}
	}
	var unavailable []PeakRSS
	job, err := childJob()
	switch {
	case err != nil:
		unavailable = append(unavailable, PeakRSS{Reason: "进程作业不可用：" + err.Error()})
	default:
		inJob, listErr := jobProcessIDs(job)
		if listErr != nil {
			unavailable = append(unavailable, PeakRSS{Reason: "枚举作业进程失败：" + listErr.Error()})
		}
		for _, pid := range inJob {
			pids[pid] = true
		}
	}
	order := make([]int, 0, len(pids))
	for pid := range pids {
		order = append(order, pid)
	}
	sort.Ints(order)
	out := make([]PeakRSS, 0, len(unavailable)+len(order))
	out = append(out, unavailable...)
	for _, pid := range order {
		out = append(out, windowsPeakRSS(pid))
	}
	return out
}

// jobProcessIDs lists the job's member PIDs. QueryInformationJobObject answers
// ERROR_MORE_DATA and fills NumberOfAssignedProcesses when the buffer is short,
// so the second call with the announced count is the normal path, not an error.
func jobProcessIDs(job windows.Handle) ([]int, error) {
	const header = 8 // two uint32: NumberOfAssignedProcesses, NumberOfProcessIdsInList
	ptrSize := int(unsafe.Sizeof(uintptr(0)))
	size := header + 64*ptrSize
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		var ret uint32
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), &ret)
		if err == windows.ERROR_MORE_DATA {
			assigned := int(binary.LittleEndian.Uint32(buf[0:4]))
			size = header + (assigned+1)*ptrSize
			continue
		}
		if err != nil {
			return nil, err
		}
		return decodeJobPIDs(buf), nil
	}
	return nil, fmt.Errorf("作业进程列表在三次查询内没有稳定下来")
}

// decodeJobPIDs reads JOBOBJECT_BASIC_PROCESS_ID_LIST out of a raw buffer. Fields
// are decoded by offset with encoding/binary rather than by casting the byte
// slice: make() does not promise the alignment an unsafe load would need.
func decodeJobPIDs(buf []byte) []int {
	if len(buf) < 8 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(buf[4:8]))
	ptrSize := int(unsafe.Sizeof(uintptr(0)))
	out := make([]int, 0, count)
	for i := 0; i < count; i++ {
		off := 8 + i*ptrSize
		if off+ptrSize > len(buf) {
			break
		}
		var pid uintptr
		if ptrSize == 8 {
			pid = uintptr(binary.LittleEndian.Uint64(buf[off : off+8]))
		} else {
			pid = uintptr(binary.LittleEndian.Uint32(buf[off : off+4]))
		}
		if pid > 0 {
			out = append(out, int(pid))
		}
	}
	return out
}

// processMemoryCountersEX mirrors PROCESS_MEMORY_COUNTERS_EX (psapi.h). Only
// PeakWorkingSetSize is read; the rest exists so cb and the kernel's layout stay
// correct. PeakWorkingSetSize is the peak of the resident set, which is the same
// quantity /proc/<pid>/status calls VmHWM.
type processMemoryCountersEX struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

// windowsPeakRSS reads one process's peak working set. An exited process cannot
// be opened; that is reported as unavailable with the reason, never as 0 bytes.
func windowsPeakRSS(pid int) PeakRSS {
	out := PeakRSS{PID: pid}
	if pid <= 0 {
		out.Reason = "无效 PID"
		return out
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		out.Reason = "打开进程失败（可能已退出）：" + err.Error()
		return out
	}
	defer windows.CloseHandle(h)
	out.Name = processImageName(h)
	var counters processMemoryCountersEX
	counters.cb = uint32(unsafe.Sizeof(counters))
	ok, _, callErr := procGetProcessMemoryInfo.Call(uintptr(h),
		uintptr(unsafe.Pointer(&counters)), uintptr(counters.cb))
	if ok == 0 {
		out.Reason = "读取峰值内存失败：" + callErr.Error()
		return out
	}
	out.Bytes, out.Available = uint64(counters.PeakWorkingSetSize), true
	return out
}

// processImageName returns the executable base name, or "" when the query is
// denied. A missing name does not invalidate the reading.
func processImageName(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:size]))
}
