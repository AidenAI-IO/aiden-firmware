package agent

import "os"

const defaultParsedMemoryCacheCapacity = 256

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

// parsedMemoryCache is a bounded cache for parsedMemoryMarkdown keyed by file
// path. Once full, cold scan entries are not admitted, so a full directory scan
// cannot evict the entries already providing value. Non-positive capacities
// disable caching. Must hold cacheMu before calling methods.
type parsedMemoryCache struct {
	capacity int
	entries  map[string]cachedParsedMemoryMarkdown
}

type cachedParsedMemoryMarkdown struct {
	signature memoryFileSignature
	parsed    parsedMemoryMarkdown
}

func (c *parsedMemoryCache) init(capacity int) {
	if capacity < 0 {
		capacity = 0
	}
	c.capacity = capacity
	c.entries = nil
	if c.capacity > 0 {
		c.entries = make(map[string]cachedParsedMemoryMarkdown, c.capacity)
	}
}

func (c *parsedMemoryCache) get(path string, signature memoryFileSignature) (parsedMemoryMarkdown, bool) {
	if c.capacity == 0 || c.entries == nil {
		return parsedMemoryMarkdown{}, false
	}
	entry, ok := c.entries[path]
	if !ok || entry.signature != signature {
		return parsedMemoryMarkdown{}, false
	}
	return cloneParsedMemoryMarkdown(entry.parsed), true
}

func (c *parsedMemoryCache) put(path string, signature memoryFileSignature, parsed parsedMemoryMarkdown) {
	if c.capacity == 0 {
		return
	}
	if c.entries == nil {
		c.init(c.capacity)
	}
	if _, exists := c.entries[path]; exists {
		c.entries[path] = cachedParsedMemoryMarkdown{signature: signature, parsed: cloneParsedMemoryMarkdown(parsed)}
		return
	}
	if len(c.entries) >= c.capacity {
		return
	}
	c.entries[path] = cachedParsedMemoryMarkdown{signature: signature, parsed: cloneParsedMemoryMarkdown(parsed)}
}

func (c *parsedMemoryCache) evict(paths ...string) {
	if c.capacity == 0 || c.entries == nil {
		return
	}
	if len(paths) == 0 {
		c.entries = nil
		return
	}
	for _, path := range paths {
		delete(c.entries, path)
	}
}

func (c *parsedMemoryCache) len() int {
	if c.capacity == 0 || c.entries == nil {
		return 0
	}
	return len(c.entries)
}

func (c *parsedMemoryCache) has(path string) bool {
	if c.capacity == 0 || c.entries == nil {
		return false
	}
	_, ok := c.entries[path]
	return ok
}
