package plasma

import (
	"github.com/t3rm1n4l/nitro/skiplist"
	"sort"
	"unsafe"
)

type Iterator struct {
	store *Plasma
	*wCtx
	currPid   PageId
	nextPid   PageId
	currPgItr pgOpIterator

	err error
}

func (s *Plasma) NewIterator() ItemIterator {
	return &Iterator{
		store: s,
		wCtx: &wCtx{
			buf:   s.Skiplist.MakeBuf(),
			slSts: &s.Skiplist.Stats,
			// TODO: merge with plasma store stats
			sts: new(Stats),
		},
	}
}

func (itr *Iterator) initPgIterator(pid PageId, seekItm unsafe.Pointer) {
	itr.currPid = pid
	if pgPtr, err := itr.store.ReadPage(pid, itr.wCtx.pgRdrFn, true); err == nil {
		pg := pgPtr.(*page)
		if !pg.IsEmpty() {
			itr.nextPid = pg.Next()
			itr.currPgItr, _ = newPgOpIterator(pg.head, pg.cmp, seekItm, pg.head.hiItm, true)
			itr.currPgItr.Init()
		} else {
			itr.err = err
		}
	}
}

func (itr *Iterator) SeekFirst() error {
	itr.initPgIterator(itr.store.Skiplist.HeadNode(), nil)
	return itr.err

}

func (itr *Iterator) Seek(itm unsafe.Pointer) error {
	var pid PageId
	if prev, curr, found := itr.store.Skiplist.Lookup(itm, itr.store.cmp, itr.wCtx.buf, itr.wCtx.slSts); found {
		pid = curr
	} else {
		pid = prev
	}
	itr.initPgIterator(pid, itm)
	return itr.err
}

func (itr *Iterator) Get() unsafe.Pointer {
	itm, _ := itr.currPgItr.Get()
	return itm
}

func (itr *Iterator) Valid() bool {
	return itr.currPgItr.Valid()
}

func (itr *Iterator) Next() error {
	itr.currPgItr.Next()
	if !itr.currPgItr.Valid() {
		if itr.nextPid != nil {
			itr.initPgIterator(itr.nextPid, nil)
		}
	}

	return itr.err
}

// Delta chain sorted iterator
type pdIterator struct {
	deltas []PageItem
	i      int
}

func (pdi *pdIterator) Init() {}

func (pdi *pdIterator) Get() (unsafe.Pointer, bool) {
	return pdi.deltas[pdi.i].Item(), pdi.deltas[pdi.i].IsInsert()
}

func (pdi *pdIterator) Valid() bool {
	return pdi.i < len(pdi.deltas)
}

func (pdi *pdIterator) Next() {
	pdi.i++
}

// Base page interator
type basePgIterator struct {
	cmp       skiplist.CompareFn
	low, high unsafe.Pointer
	bp        *basePage
	i, j      int
}

func (bpi *basePgIterator) Init() {
	n := len(bpi.bp.items)
	bpi.i = sort.Search(n, func(i int) bool {
		return bpi.cmp(bpi.bp.items[i], bpi.low) >= 0
	})

	bpi.j = sort.Search(n, func(i int) bool {
		return bpi.cmp(bpi.bp.items[i], bpi.high) >= 0
	})
}

func (bpi *basePgIterator) Get() (unsafe.Pointer, bool) {
	return bpi.bp.items[bpi.i], true
}

func (bpi *basePgIterator) Valid() bool {
	return bpi.i < bpi.j
}

func (bpi *basePgIterator) Next() {
	bpi.i++
}

// Merge two disjoint sorted sets
type pdJoinIterator struct {
	itrs [2]pgOpIterator
	i    int

	currIt pgOpIterator
}

func (pdj *pdJoinIterator) Init() {
	pdj.itrs[0].Init()
	pdj.itrs[1].Init()
}

func (pdj *pdJoinIterator) Valid() bool {
	return pdj.itrs[pdj.i].Valid()
}

func (pdj *pdJoinIterator) Next() {
	pdj.itrs[pdj.i].Next()
	if pdj.i == 0 && !pdj.itrs[pdj.i].Valid() {
		pdj.i++
	}
}

func (pdj *pdJoinIterator) Get() (unsafe.Pointer, bool) {
	return pdj.itrs[pdj.i].Get()
}

// Iterator merger
type pdMergeIterator struct {
	itrs    [2]pgOpIterator
	lastIt  pgOpIterator
	cmp     skiplist.CompareFn
	doDedup bool
}

func (pdm *pdMergeIterator) Init() {
	pdm.itrs[0].Init()
	pdm.itrs[1].Init()
	pdm.fetchMin()
}

func (pdm *pdMergeIterator) Next() {
	if pdm.Valid() {
		pdm.lastIt.Next()
		pdm.fetchMin()
	}
}

func (pdm *pdMergeIterator) fetchMin() {
	valid1 := pdm.itrs[0].Valid()
	valid2 := pdm.itrs[1].Valid()

	if valid1 && valid2 {
		itm0, _ := pdm.itrs[0].Get()
		itm1, _ := pdm.itrs[1].Get()

		cmpv := pdm.cmp(itm0, itm1)
		if cmpv < 0 {
			pdm.lastIt = pdm.itrs[0]
		} else if cmpv == 0 {
			pdm.lastIt = pdm.itrs[0]
			pdm.itrs[1].Next()
		} else {
			pdm.lastIt = pdm.itrs[1]
		}
	} else if valid1 {
		pdm.lastIt = pdm.itrs[0]
	} else if valid2 {
		pdm.lastIt = pdm.itrs[1]
	}

	// Skiplist delete deltas
	if pdm.doDedup && pdm.Valid() {
		if _, ok := pdm.lastIt.Get(); !ok {
			pdm.Next()
		}
	}
}

func (pdm *pdMergeIterator) Get() (unsafe.Pointer, bool) {
	if pdm.lastIt == nil {
		return nil, false
	}
	return pdm.lastIt.Get()
}

func (pdm *pdMergeIterator) Valid() bool {
	return pdm.itrs[0].Valid() || pdm.itrs[1].Valid()
}

type pgOpIterator interface {
	Init()
	Get() (unsafe.Pointer, bool)
	Next()
	Valid() bool
}

func newPgOpIterator(pd *pageDelta, cmp skiplist.CompareFn,
	low, high unsafe.Pointer, doDedup bool) (iter pgOpIterator, fdSz int) {

	var hasReloc bool
	m := &pdMergeIterator{cmp: cmp, doDedup: doDedup}
	startPd := pd
	pdCount := 0

	pdi := &pdIterator{}
loop:
	for pd != nil {
		switch pd.op {
		case opRelocPageDelta:
			fpd := (*flushPageDelta)(unsafe.Pointer(pd))
			if !hasReloc {
				fdSz = int(fpd.flushDataSz)
				hasReloc = true
			}
		case opFlushPageDelta:
			if !hasReloc {
				fpd := (*flushPageDelta)(unsafe.Pointer(pd))
				fdSz += int(fpd.flushDataSz)
			}
		case opPageSplitDelta:
			high = (*splitPageDelta)(unsafe.Pointer(pd)).itm
		case opPageMergeDelta:
			deltaItr, fdSz1 := newPgOpIterator(pd.next, cmp, low, high, false)
			mergeItr, fdSz2 := newPgOpIterator(
				(*mergePageDelta)(unsafe.Pointer(pd)).mergeSibling,
				cmp, low, high, true)

			if !hasReloc {
				fdSz += fdSz1 + fdSz2
			}

			m.itrs[1] = &pdJoinIterator{
				itrs: [2]pgOpIterator{deltaItr, mergeItr},
			}
			break loop
		case opBasePage:
			m.itrs[1] = &basePgIterator{
				bp:   (*basePage)(unsafe.Pointer(pd)),
				cmp:  cmp,
				low:  low,
				high: high,
			}

			break loop
		case opInsertDelta, opDeleteDelta:
			pdCount++
		}

		pd = pd.next
	}

	if pdCount > 0 {
		pdi.deltas = make([]PageItem, 0, pdCount)
		for x := startPd; x != pd; x = x.next {
			if x.op == opInsertDelta || x.op == opDeleteDelta {
				rec := (*recordDelta)(unsafe.Pointer(x))
				if cmp(rec.itm, high) < 0 && cmp(rec.itm, low) >= 0 {
					pdi.deltas = append(pdi.deltas, x)
				}
			}
		}

		s := pageItemSorter{itms: pdi.deltas, cmp: cmp}
		pdi.deltas = s.Run()
	}
	m.itrs[0] = pdi
	if m.itrs[1] == nil {
		m.itrs[1] = &pdIterator{}
	}

	return m, fdSz
}
