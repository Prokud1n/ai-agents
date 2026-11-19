package extract

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const (
	vmSizePrefix = "VmSize:"
	kibibyte     = 1024
	decimalBase  = 10
	uint64Bits   = 64
)

// limitAddressSpace caps the process's virtual memory (RLIMIT_AS) at what is mapped now plus extra.
// Go reserves address space at start, so a fixed cap could break the runtime; relative to the current
// size it bounds only what the parser allocates. Past the cap allocations fail and the runtime exits
// with "out of memory".
func limitAddressSpace(extra uint64) error {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("failed to read /proc/self/status: %w", err)
	}

	size, err := vmSizeBytes(status)
	if err != nil {
		return err
	}

	limit := size + extra
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: limit, Max: limit}); err != nil {
		return fmt.Errorf("failed to set RLIMIT_AS: %w", err)
	}

	return nil
}

// vmSizeBytes reads "VmSize:   123456 kB" from /proc/self/status.
func vmSizeBytes(status []byte) (uint64, error) {
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		value, ok := strings.CutPrefix(sc.Text(), vmSizePrefix)
		if !ok {
			continue
		}

		kib, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), " kB"), decimalBase, uint64Bits)
		if err != nil {
			return 0, fmt.Errorf("failed to parse VmSize: %w", err)
		}

		return kib * kibibyte, nil
	}

	return 0, errors.New("no VmSize in /proc/self/status")
}
