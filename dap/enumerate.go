package dap

import "context"

const apEnumerationChunk = 8

// APInfo identifies one implemented ADIv5 access port.
type APInfo struct {
	Selector APSel
	Identity APIDRInfo
}

// EnumerateAPs scans every ADIv5 APSEL value. An IDR of zero is absent; the
// scan does not assume that implemented access ports are contiguous. On
// failure it returns the discoveries confirmed before the failing access.
func (dp *DebugPort) EnumerateAPs(ctx context.Context) ([]APInfo, error) {
	if err := dp.requireConnected(); err != nil {
		return nil, err
	}
	var found []APInfo
	for start := 0; start < 256; start += apEnumerationChunk {
		chunk, err := dp.enumerateAPChunk(ctx, start, min(start+apEnumerationChunk, 256))
		found = append(found, chunk...)
		if err != nil {
			return found, err
		}
	}
	return found, nil
}

func (dp *DebugPort) enumerateAPChunk(ctx context.Context, start, end int) ([]APInfo, error) {
	txn := dp.NewTxn()
	results := make([]*ReadResult, end-start)
	for sel := start; sel < end; sel++ {
		results[sel-start] = txn.ReadAPIDR(NewAPSel(uint8(sel)))
	}
	commitErr := txn.Commit(ctx)
	var found []APInfo
	for i := range results {
		value, err := results[i].Value()
		if err != nil {
			if commitErr != nil {
				return found, commitErr
			}
			return found, err
		}
		if value != 0 {
			found = append(found, APInfo{Selector: NewAPSel(uint8(start + i)), Identity: DecodeAPIDR(value)})
		}
	}
	return found, commitErr
}
