package gopdf

import (
	"fmt"
)

// Reordering and removing pages during an incremental update.
//
// The page tree is rewritten as a single flat node listing the pages in
// their new order. Attributes a page used to inherit from an intermediate
// node — its resources, boxes and rotation — are written onto the page
// itself first, so flattening cannot change how anything renders.

// SetPageOrder rewrites the document's page order. The argument lists the
// 0-based indexes of the pages to keep, in the order they should appear;
// pages left out are removed from the tree.
//
// The page objects themselves stay in the file, so this is cheap and
// reversible by another update.
func (u *Updater) SetPageOrder(order []int) error {
	if len(order) == 0 {
		return fmt.Errorf("gopdf: a document needs at least one page")
	}
	seen := make(map[int]bool, len(order))
	for _, idx := range order {
		if idx < 0 || idx >= u.r.NumPages() {
			return fmt.Errorf("gopdf: page %d out of range (document has %d pages)",
				idx, u.r.NumPages())
		}
		if seen[idx] {
			return fmt.Errorf("gopdf: page %d appears twice in the new order", idx)
		}
		seen[idx] = true
		if _, ok := u.r.pageObjectNumber(idx); !ok {
			return fmt.Errorf("gopdf: page %d is not an indirect object", idx)
		}
	}
	u.pageOrder = append([]int(nil), order...)
	return nil
}

// RemovePage drops a page from the document.
func (u *Updater) RemovePage(index int) error {
	order, err := u.currentOrder()
	if err != nil {
		return err
	}
	pos := indexOf(order, index)
	if pos < 0 {
		return fmt.Errorf("gopdf: page %d is not in the document", index)
	}
	if len(order) == 1 {
		return fmt.Errorf("gopdf: cannot remove the only page")
	}
	return u.SetPageOrder(append(order[:pos:pos], order[pos+1:]...))
}

// MovePage moves a page to a new position, counted in the document's
// current order.
func (u *Updater) MovePage(from, to int) error {
	order, err := u.currentOrder()
	if err != nil {
		return err
	}
	pos := indexOf(order, from)
	if pos < 0 {
		return fmt.Errorf("gopdf: page %d is not in the document", from)
	}
	if to < 0 || to >= len(order) {
		return fmt.Errorf("gopdf: position %d out of range (document has %d pages)",
			to, len(order))
	}
	rest := append(order[:pos:pos], order[pos+1:]...)
	out := make([]int, 0, len(order))
	out = append(out, rest[:to]...)
	out = append(out, from)
	out = append(out, rest[to:]...)
	return u.SetPageOrder(out)
}

// currentOrder is the page order as it stands, defaulting to the source
// document's own.
func (u *Updater) currentOrder() ([]int, error) {
	if u.pageOrder != nil {
		return append([]int(nil), u.pageOrder...), nil
	}
	order := make([]int, u.r.NumPages())
	for i := range order {
		order[i] = i
	}
	return order, nil
}

func indexOf(list []int, v int) int {
	for i, e := range list {
		if e == v {
			return i
		}
	}
	return -1
}

// rebuildPageTree writes the flattened /Pages node and pins each surviving
// page's inherited attributes onto the page itself.
func (u *Updater) rebuildPageTree() error {
	if u.pageOrder == nil {
		return nil
	}
	root, _ := u.r.resolve(u.r.trailer["Root"]).(Dict)
	pagesRef, ok := root["Pages"].(Ref)
	if !ok {
		return fmt.Errorf("gopdf: the page tree root is not an indirect object")
	}
	pagesDict, ok := u.r.resolve(pagesRef).(Dict)
	if !ok {
		return fmt.Errorf("gopdf: malformed page tree root")
	}

	kids := make(Array, 0, len(u.pageOrder))
	for _, idx := range u.pageOrder {
		num, _ := u.r.pageObjectNumber(idx)
		pi := u.r.pages[idx]
		dict := cloneDict(u.pageDict(num, idx))
		// Attributes that used to come from an ancestor must now be
		// spelled out, since the tree they came from is gone.
		dict["Parent"] = pagesRef
		if _, ok := dict["Resources"]; !ok && pi.resources != nil {
			dict["Resources"] = pi.resources
		}
		if _, ok := dict["MediaBox"]; !ok {
			dict["MediaBox"] = Array{
				pi.mediaBox[0], pi.mediaBox[1], pi.mediaBox[2], pi.mediaBox[3],
			}
		}
		if _, ok := dict["Rotate"]; !ok && pi.rotate != 0 {
			dict["Rotate"] = int64(pi.rotate)
		}
		u.set(num, dict)
		kids = append(kids, Ref{Num: num})
	}

	newRoot := cloneDict(pagesDict)
	newRoot["Kids"] = kids
	newRoot["Count"] = int64(len(kids))
	// Inheritable attributes on the root would now apply to every page,
	// and each page already carries its own.
	for _, k := range []Name{"Resources", "MediaBox", "CropBox", "Rotate"} {
		delete(newRoot, k)
	}
	u.set(pagesRef.Num, newRoot)
	return nil
}
