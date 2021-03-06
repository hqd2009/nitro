package plasma

import (
	"github.com/couchbase/nitro/skiplist"
	"reflect"
	"sort"
	"unsafe"
)

func memcopy(dst, src unsafe.Pointer, sz int) {
	var sb, db []byte
	hdrsb := (*reflect.SliceHeader)(unsafe.Pointer(&sb))
	hdrsb.Len = sz
	hdrsb.Cap = hdrsb.Len
	hdrsb.Data = uintptr(src)

	hdrdb := (*reflect.SliceHeader)(unsafe.Pointer(&db))
	hdrdb.Len = sz
	hdrdb.Cap = hdrdb.Len
	hdrdb.Data = uintptr(dst)
	copy(db, sb)
}

type pageItemSorter struct {
	itms []PageItem
	cmp  skiplist.CompareFn
}

func (s *pageItemSorter) Run() []PageItem {
	sort.Stable(s)
	return s.itms
}

func (s *pageItemSorter) Len() int {
	return len(s.itms)
}

func (s *pageItemSorter) Less(i, j int) bool {
	return s.cmp(s.itms[i].Item(), s.itms[j].Item()) < 0
}

func (s *pageItemSorter) Swap(i, j int) {
	s.itms[i], s.itms[j] = s.itms[j], s.itms[i]
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}

	return b
}
