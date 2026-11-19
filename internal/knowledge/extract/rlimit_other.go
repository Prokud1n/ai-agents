//go:build !linux

package extract

// limitAddressSpace is a no-op outside Linux: there the child is bounded by GOMEMLIMIT and the timeout only.
func limitAddressSpace(uint64) error {
	return nil
}
