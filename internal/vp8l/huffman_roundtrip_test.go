package vp8l

import (
	"reflect"
	"testing"
)

func TestHuffmanWriteRead(t *testing.T) {
	cases := [][]uint32{
		{1},
		{0, 5},
		{3, 1, 1, 0, 2},
		{1, 1, 1, 1, 1, 1, 1, 1},
		{10, 5, 3, 1, 1, 1, 1, 1},
	}
	for ci, freq := range cases {
		h := newHuffmanCode(freq, 15)
		e := &encoder{}
		e.writeHuffmanCode(h)
		data := e.w.finish()
		br := newBitReader(data)
		got, err := readHuffmanCode(br, len(freq))
		if err != nil {
			t.Fatalf("case %d: %v (data % x)", ci, err, data)
		}
		if got.single >= 0 {
			if h.single && int(got.single) != indexOfNonZero(h.lengths) {
				t.Fatalf("case %d: single symbol %d, want %d", ci, got.single, indexOfNonZero(h.lengths))
			}
			continue
		}
		// compare the decoded symbol table against the written code
		wantTree, err := buildHuffmanTree(h.lengths)
		if err != nil {
			t.Fatalf("case %d: %v", ci, err)
		}
		if !reflect.DeepEqual(got.counts, wantTree.counts) || !reflect.DeepEqual(got.symbols, wantTree.symbols) {
			t.Fatalf("case %d: got %v/%v, want %v/%v", ci, got.counts, got.symbols, wantTree.counts, wantTree.symbols)
		}
	}
}

func indexOfNonZero(l []uint32) int {
	for i, v := range l {
		if v > 0 {
			return i
		}
	}
	return -1
}
