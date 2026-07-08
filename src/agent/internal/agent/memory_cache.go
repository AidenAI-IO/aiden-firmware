package agent

import (
	"os"
)

type memoryFileSignature struct {
	ModTime int64
	Size    int64
}

func memoryFileSignatureForPath(path string) (memoryFileSignature, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return memoryFileSignature{}, false, nil
		}
		return memoryFileSignature{}, false, err
	}
	return memoryFileSignature{
		ModTime: info.ModTime().UnixNano(),
		Size:    info.Size(),
	}, true, nil
}

func cloneMemorySourceRefs(refs []MemorySourceRef) []MemorySourceRef {
	if len(refs) == 0 {
		return nil
	}
	cloned := make([]MemorySourceRef, len(refs))
	for i, ref := range refs {
		cloned[i] = ref
		cloned[i].EventIDs = append([]string(nil), ref.EventIDs...)
	}
	return cloned
}
